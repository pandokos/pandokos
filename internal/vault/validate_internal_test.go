package vault

import (
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// TestValidatorTransport pins the transport of the validator: no proxy from the environment
// (T-307) and no redirect followed.
func TestValidatorTransport(t *testing.T) {
	u, _ := url.Parse("http://192.0.2.1:9")
	v, ok := NewOpenAIValidator(u, time.Second).(*openAIValidator)
	if !ok {
		t.Fatal("not an openAIValidator")
	}
	tr, ok := v.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport %T", v.client.Transport)
	}
	if tr.Proxy != nil {
		t.Error("the validator takes a proxy from the environment")
	}
	if !tr.DisableKeepAlives {
		t.Error("connections are reused")
	}
	if err := v.client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Errorf("CheckRedirect = %v, want ErrUseLastResponse", err)
	}
}
