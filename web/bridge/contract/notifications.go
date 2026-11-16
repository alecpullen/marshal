package contract

// The notification routes (web/bridge/notify.go): where Marshal reaches the
// user, and the test that proves it can.

// NotifyWebhook is one outbound notification target. Its signing secret is a
// reference: the secret itself is never returned.
type NotifyWebhook struct {
	ID        string   `json:"id"`
	URL       string   `json:"url"`
	SecretRef string   `json:"secretRef,omitempty"`
	Events    []string `json:"events"`
}

// NotifyConfig is GET and PUT /api/notifications' body: the webhooks.
type NotifyConfig struct {
	Webhooks []NotifyWebhook `json:"webhooks"`
}

// NotifyTestResult is POST /api/notifications/test's answer: how many webhooks
// the test was queued for.
type NotifyTestResult struct {
	Sent int `json:"sent"`
}
