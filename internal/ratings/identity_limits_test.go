package ratings

import (
	"fmt"
	"strings"
	"testing"
)

func TestRatingTargetVersionCharacterLimits(t *testing.T) {
	t.Parallel()
	target := Target{Origin: "foreign", SourceID: "00000000-0000-7000-8000-000000000001", PresentationGeneration: 1, Identifier: "urn:air:remote.example:agents:unicode", Kind: "agent", VersionPresent: true}
	for _, character := range []string{"x", "é", "🙂"} {
		for _, count := range []int{127, 128, 129} {
			t.Run(fmt.Sprintf("%s/%d", character, count), func(t *testing.T) {
				target.Version = strings.Repeat(character, count)
				if err := ValidateTarget(target); (err == nil) != (count <= 128) {
					t.Fatalf("version admission: %v", err)
				}
			})
		}
	}
	for _, value := range []string{"bad\x00text", "bad\xfftext", "bad\ntext", "bad\rtext"} {
		target.Version = value
		if ValidateTarget(target) == nil {
			t.Fatalf("unsafe version accepted: %q", value)
		}
	}
	target.Version = strings.Repeat("🙂", 128)
	key := Key(target)
	target.Version += "x"
	if Key(target) == key {
		t.Fatal("different exact versions have the same target key")
	}
}
