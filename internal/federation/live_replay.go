package federation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type liveReplayFetch struct {
	Policy     string `json:"policy"`
	Credential string `json:"credential"`
	Endpoint   string `json:"endpoint"`
	More       bool   `json:"more"`
}

// replayFingerprint covers successful empty peers as well as contributing
// peers. Fetch-time evidence, not the cached source credential revision, owns
// the access binding. Tokens, receipts and tracing IDs are deliberately absent.
func (m *Manager) replayFingerprint(ctx context.Context, selected []Source, peers []*liveSearchPeer, response LiveSearchResponse) string {
	if ctx.Err() != nil {
		return ""
	}
	for _, d := range response.Diagnostics {
		switch d.Status {
		case "succeeded", "fanout_limited", "continuation_unavailable", "insufficient_evidence":
		default:
			return ""
		}
	}
	type topology struct {
		ID            string `json:"id"`
		Configuration string `json:"configuration"`
		Revision      int64  `json:"revision"`
	}
	type traversal struct {
		Source  topology          `json:"source"`
		Depth   int               `json:"depth"`
		Pages   int               `json:"pages"`
		Fetches []liveReplayFetch `json:"fetches"`
	}
	type candidate struct {
		Identifier     string `json:"identifier"`
		Version        string `json:"version"`
		SourceID       string `json:"sourceId"`
		Source         string `json:"source"`
		Digest         string `json:"digest"`
		VersionPresent bool   `json:"versionPresent"`
		Score          int    `json:"score"`
	}
	state := struct {
		Domain      string      `json:"domain"`
		Fanout      int         `json:"fanout"`
		Hops        int         `json:"hops"`
		Pages       int         `json:"pages"`
		Selected    []topology  `json:"selected"`
		Traversal   []traversal `json:"traversal"`
		Candidates  []candidate `json:"candidates"`
		Partial     bool        `json:"partial"`
		Disposition []string    `json:"disposition"`
	}{Domain: "live-client-window-v1", Fanout: m.maxFanout, Hops: m.maxHops, Pages: m.maxLivePagesPerSource, Partial: response.Partial}
	for _, source := range selected {
		digest, err := sourceConfigurationDigest(source)
		if err != nil {
			return ""
		}
		state.Selected = append(state.Selected, topology{source.ID, digest, source.Revision})
	}
	for _, peer := range peers {
		if !peer.validPage || peer.invalid || peer.access.operationError() != nil || len(peer.replayFetches) != peer.pages {
			return ""
		}
		state.Traversal = append(state.Traversal, traversal{topology{peer.source.ID, peer.configurationDigest, peer.source.Revision}, peer.depth, peer.pages, peer.replayFetches})
	}
	for _, result := range response.Results {
		digest := sha256.Sum256(result.Entry)
		state.Candidates = append(state.Candidates, candidate{result.Identifier, result.Version, result.SourceID, result.Source, hex.EncodeToString(digest[:]), result.VersionPresent, result.Score})
	}
	for _, d := range response.Diagnostics {
		state.Disposition = append(state.Disposition, d.SourceID+"\x00"+d.Status)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
