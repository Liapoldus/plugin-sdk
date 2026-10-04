package models

import "strings"

// AdminActionInvocation is the product-neutral context Core authorizes for one
// plugin Admin Surface action. Values are opaque identifiers; product meaning
// remains with the plugin.
type AdminActionInvocation struct {
	CallerID       string `json:"callerId"`
	InstanceID     string `json:"instanceId"`
	PageID         string `json:"pageId"`
	ActionID       string `json:"actionId"`
	SurfaceDigest  string `json:"surfaceDigest"`
	RequestID      string `json:"requestId"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	IfMatch        string `json:"ifMatch,omitempty"`
}

func (invocation AdminActionInvocation) Validate() error {
	for _, value := range []string{invocation.CallerID, invocation.InstanceID, invocation.PageID,
		invocation.ActionID, invocation.SurfaceDigest, invocation.RequestID} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n") {
			return ErrInvalidAdminActionInvocation
		}
	}
	for _, value := range []string{invocation.IdempotencyKey, invocation.IfMatch} {
		if strings.ContainsAny(value, "\r\n") {
			return ErrInvalidAdminActionInvocation
		}
	}
	return nil
}
