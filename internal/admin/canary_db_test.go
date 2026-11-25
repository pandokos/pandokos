package admin_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
)

// TestPeopleCanary is T-837, at the level of the handler: values that must never be written
// anywhere are planted in sign-ins of both methods, accepted and refused, with the log at debug, and
// every output is searched for them.
func TestPeopleCanary(t *testing.T) {
	t.Parallel()
	api, o := apitest.NewPeopleOIDC(t, apitest.PeopleConfig{}, apitest.OIDCConfig{
		Users: idpPeople, Mappings: []string{"platform:platform-admin=admins"}, AutoProvision: true,
	})
	p := peopleOn(t, api)
	d := p.d

	type output struct{ name, text string }
	var responses []output
	record := func(name string, r *apitest.Response) *apitest.Response {
		var h strings.Builder
		for k, v := range r.Header {
			fmt.Fprintf(&h, "%s: %s\n", k, strings.Join(v, ","))
		}
		responses = append(responses, output{name, h.String() + string(r.Body)})
		return r
	}
	// secrets maps each planted value to the one response that may show it.
	secrets := map[string]string{}
	plant := func(value, shownBy string) string { secrets[value] = shownBy; return value }

	typedUser := plant(apitest.Canary("username"), "")
	typedPassword := plant(apitest.Canary("password"), "")
	known := d.PlatformUser(t, "canary.person", []string{"platform-admin"})
	plant(known.Password, "")

	// Local sign-ins: refused (unknown user, wrong password), accepted.
	record("unknown user", api.NewBrowser().SignIn(t, typedUser, typedPassword))
	record("wrong password", api.NewBrowser().SignIn(t, known.Username, typedPassword))
	b := api.NewBrowser()
	in := record("sign-in", b.SignIn(t, known.Username, known.Password))
	if in.Status != 200 {
		t.Fatalf("sign-in: %d %s", in.Status, in.Body)
	}
	token := plant(b.Token(), "sign-in") // the cookie is the one place a token is shown
	var hash string
	if err := d.Super(t).QueryRow(ctx(t), "SELECT password_hash FROM pandokos.local_account WHERE user_id = $1::uuid", known.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	plant(hash, "")

	// A password change: the current and the new password.
	next := plant(dbtest.NewPassword(t), "")
	record("change", b.Do(t, "POST", "/v1/auth/password", map[string]any{"current_password": known.Password, "new_password": next}, nil))
	record("wrong current", b.Do(t, "POST", "/v1/auth/password", map[string]any{"current_password": typedPassword, "new_password": plant(dbtest.NewPassword(t), "")}, nil))

	// A generated password and a reset.
	created := decode[adminapi.UserCreated](t, record("createUser", p.platform(t, "POST", "/v1/users", map[string]any{"sign_in": "local", "username": "canary.created"})), 201)
	plant(created.InitialPassword, "createUser")
	reset := decode[adminapi.PasswordReset](t, record("reset", p.platform(t, "POST", "/v1/users/"+created.User.ID+"/reset-password", map[string]any{"reason": "test"})), 200)
	plant(reset.Password, "reset")
	member := decode[adminapi.MemberCreated](t, record("createMember", p.tenantKey(t, "POST", p.membersPath(""), map[string]any{"account": "new", "username": "canary.member", "roles": []string{"tenant-admin"}})), 201)
	plant(member.InitialPassword, "createMember")

	// The identity provider: an accepted sign-in (a new user by group rule), and refused ones.
	plant(o.IDP.ClientSecret(), "")
	plant(o.IDP.AccessToken(), "")
	plant(o.IDP.RefreshToken(), "")
	ob := api.NewBrowser()
	o.IDP.SignInAs("ada")
	start := o.Start(t, ob)
	seed := ob.Cookie("__Host-pandokos_oidc")
	plant(seed, "start") // the binding cookie is set by the start, and nowhere else
	q := o.Authorize(t, start.Location)
	plant(o.IDP.LastCode(), "") // the code travels in the redirect of the provider, not in a response of ours
	hops := o.Callback(t, ob, q)
	if end := last(hops); end.Location != "/console" {
		t.Fatalf("the sign-in through the provider ends with %d %q", end.Status, end.Location)
	}
	for i, h := range hops {
		responses = append(responses, output{fmt.Sprintf("callback hop %d", i), h.Header.Get("Location") + fmt.Sprint(h.Header) + string(h.Body)})
	}
	plant(ob.Token(), "callback hop 0") // the first hop is the callback itself, which sets the cookie
	plant(o.IDP.LastIDToken(), "")
	// A refused one: the provider reports an error; and a replayed state.
	o.IDP.SignInAs("dan")
	rb := api.NewBrowser()
	for _, h := range o.SignIn(t, rb) {
		responses = append(responses, output{"refused sign-in through the provider", h.Location + fmt.Sprint(h.Header) + string(h.Body)})
	}
	plant(o.IDP.LastCode(), "")
	plant(o.IDP.LastIDToken(), "")

	// Search every output.
	logs := api.Logs.String()
	rows := d.AllAuditText(t)
	var tables strings.Builder
	for _, tbl := range []string{"idempotency_key", "signin_throttle", "oidc_login"} {
		var text string
		_ = d.Super(t).QueryRow(ctx(t), "SELECT COALESCE(string_agg(to_jsonb(x)::text, ' '), '') FROM pandokos."+tbl+" x").Scan(&text)
		tables.WriteString(text)
	}
	for value, shownBy := range secrets {
		if value == "" {
			continue
		}
		short := value[:min(len(value), 12)] + "..."
		if strings.Contains(logs, value) {
			t.Errorf("the log holds a planted value (%s)", short)
		}
		if strings.Contains(rows, value) {
			t.Errorf("an audit row holds a planted value (%s)", short)
		}
		if strings.Contains(tables.String(), value) {
			t.Errorf("idempotency_key, signin_throttle or oidc_login holds a planted value (%s)", short)
		}
		for _, r := range responses {
			if strings.Contains(r.text, value) && r.name != shownBy {
				t.Errorf("the response %q shows a planted value (%s) that only %q may show", r.name, short, shownBy)
			}
		}
	}
	if token == "" || seed == "" {
		t.Error("the canary run planted no token or seed")
	}
	// The typed username is never an attribute of a log line or an audit row either.
	if strings.Contains(logs, typedUser) || strings.Contains(rows, typedUser) {
		t.Error("the typed username was written")
	}
}
