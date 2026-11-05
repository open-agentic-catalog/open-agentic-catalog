// Package webhook owns optional endpoint administration and signed delivery.
// [Service] manages tenant endpoints and signing secrets; [Dispatcher] consumes
// leased public outbox events with bounded retries and claim-fenced evidence.
// Receiver acceptance cannot commit atomically with local delivery state, so
// notifications are at least once and unordered. [Drainer] acknowledges events
// without sending them when delivery is disabled.
package webhook

import "time"

type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

type Endpoint struct {
	ID               string    `json:"id"`
	TenantID         string    `json:"tenantId"`
	Name             string    `json:"name"`
	URL              string    `json:"url"`
	EventTypes       []string  `json:"eventTypes"`
	Status           Status    `json:"status"`
	Revision         int64     `json:"revision"`
	SecretVersion    int       `json:"secretVersion"`
	SecretCiphertext []byte    `json:"-"`
	CreatedBy        string    `json:"createdBy"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type EndpointListRequest struct {
	Name      string
	Status    Status
	AfterName string
	AfterID   string
	Limit     int
}

type CreateInput struct {
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	EventTypes []string `json:"eventTypes"`
}

type CreateResult struct {
	Endpoint                Endpoint   `json:"endpoint"`
	Secret                  string     `json:"secret"`
	PreviousSecretExpiresAt *time.Time `json:"previousSecretExpiresAt,omitempty"`
}

type RotateInput struct {
	RevokePrevious bool `json:"revokePrevious,omitempty"`
}

type PatchInput struct {
	Name       *string   `json:"name,omitempty"`
	URL        *string   `json:"url,omitempty"`
	EventTypes *[]string `json:"eventTypes,omitempty"`
}

type DeliveryState string

const (
	DeliveryPending    DeliveryState = "pending"
	DeliverySucceeded  DeliveryState = "succeeded"
	DeliveryWaiting    DeliveryState = "waiting"
	DeliveryDeadLetter DeliveryState = "dead_letter"
)

type Delivery struct {
	ID             string        `json:"id"`
	TenantID       string        `json:"tenantId"`
	EndpointID     string        `json:"endpointId"`
	EventID        string        `json:"eventId"`
	EventType      string        `json:"eventType"`
	State          DeliveryState `json:"state"`
	Attempt        int           `json:"attempt"`
	ResponseStatus int           `json:"responseStatus,omitempty"`
	SafeError      string        `json:"safeError,omitempty"`
	NextAttemptAt  *time.Time    `json:"nextAttemptAt,omitempty"`
	CreatedAt      time.Time     `json:"createdAt"`
	UpdatedAt      time.Time     `json:"updatedAt"`
}

type DeliveryListRequest struct {
	EndpointID     string
	DeliveryID     string
	EventID        string
	State          DeliveryState
	AfterUpdatedAt *time.Time
	AfterID        string
	Limit          int
}

type SendRequest struct {
	TenantID string
	URL      string
	Body     []byte
	Headers  map[string]string
}

type SendResult struct {
	Status     int
	RetryAfter string
}
