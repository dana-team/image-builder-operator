package git

import (
	"net/http"
)

type pushEvent struct {
	cloneURLs []string
	ref       string
	commitSHA string
}

type webhookProvider interface {
	Name() string
	Detect(r *http.Request) bool
	ReadPushEvent(body []byte) (*pushEvent, error)
	Authenticate(r *http.Request, body []byte, secret []byte) error
}
