package config

import (
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
)

// Auth configures sign-in of people on the administration listener (sprint 4.2): local accounts,
// sessions and the OpenID Connect relying party. Only the keys of Local are read by every
// command that hashes a password (init, user reset-password); the rest is read by serve.
type Auth struct {
	// PublicOrigin is the origin that browsers use to reach the administration API
	// (https://host[:port], or http:// with a loopback host); empty: derived from the request.
	PublicOrigin string      `yaml:"public_origin"`
	Local        AuthLocal   `yaml:"local"`
	Session      AuthSession `yaml:"session"`
	OIDC         AuthOIDC    `yaml:"oidc"`
}

// AuthLocal configures local accounts: password policy, throttling and the Argon2id parameters.
type AuthLocal struct {
	Enabled              bool       `yaml:"enabled"`
	PasswordMinLength    int        `yaml:"password_min_length"`
	GeneratedPasswordTTL Duration   `yaml:"generated_password_ttl"`
	MaxFailures          int        `yaml:"max_failures"`
	FailureWindow        Duration   `yaml:"failure_window"`
	LockDuration         Duration   `yaml:"lock_duration"`
	SourceMaxFailures    int        `yaml:"source_max_failures"`
	Argon2               AuthArgon2 `yaml:"argon2"`
	MaxConcurrentHashes  int        `yaml:"max_concurrent_hashes"`
}

// AuthArgon2 holds the Argon2id parameters used for new hashes.
type AuthArgon2 struct {
	MemoryKiB   int `yaml:"memory_kib"`
	Iterations  int `yaml:"iterations"`
	Parallelism int `yaml:"parallelism"`
}

// AuthSession configures the lifetime of sessions.
type AuthSession struct {
	IdleTimeout      Duration `yaml:"idle_timeout"`
	AbsoluteLifetime Duration `yaml:"absolute_lifetime"`
	MaxPerUser       int      `yaml:"max_per_user"`
}

// AuthOIDC configures the OpenID Connect relying party.
type AuthOIDC struct {
	Enabled          bool     `yaml:"enabled"`
	Issuer           string   `yaml:"issuer"`
	ClientID         string   `yaml:"client_id"`
	ClientSecretFile string   `yaml:"client_secret_file"`
	ClientAuth       string   `yaml:"client_auth"`
	Scopes           []string `yaml:"scopes"`
	Algorithms       []string `yaml:"algorithms"`
	DisplayName      string   `yaml:"display_name"`
	NameClaim        string   `yaml:"name_claim"`
	EmailClaim       string   `yaml:"email_claim"`
	GroupsClaim      string   `yaml:"groups_claim"`
	GroupMappings    []string `yaml:"group_mappings"`
	AutoProvision    bool     `yaml:"auto_provision"`
	PrivateHosts     []string `yaml:"private_hosts"`
	PostSignInPath   string   `yaml:"post_sign_in_path"`

	ClientSecret secret.Secret `yaml:"-"` // read from ClientSecretFile when the purpose is serve and OIDC is enabled
}

// Idempotency configures the retention of idempotency keys.
type Idempotency struct {
	Retention Duration `yaml:"retention"`
}

func authDefaults() Auth {
	return Auth{
		Local: AuthLocal{
			Enabled:              true,
			PasswordMinLength:    12,
			GeneratedPasswordTTL: Duration(72 * time.Hour),
			MaxFailures:          10,
			FailureWindow:        Duration(15 * time.Minute),
			LockDuration:         Duration(15 * time.Minute),
			SourceMaxFailures:    50,
			Argon2:               AuthArgon2{MemoryKiB: 65536, Iterations: 3, Parallelism: 1},
			MaxConcurrentHashes:  4,
		},
		Session: AuthSession{IdleTimeout: Duration(30 * time.Minute), AbsoluteLifetime: Duration(12 * time.Hour), MaxPerUser: 10},
		OIDC: AuthOIDC{
			ClientAuth:     "basic",
			Scopes:         []string{"openid", "profile", "email"},
			Algorithms:     []string{"RS256", "ES256"},
			DisplayName:    "Single sign-on",
			NameClaim:      "name",
			EmailClaim:     "email",
			GroupsClaim:    "groups",
			GroupMappings:  []string{},
			PrivateHosts:   []string{},
			PostSignInPath: "/",
		},
	}
}

// listValue splits a list given in the environment: entries are separated by commas.
func listValue(v string) []string { return strings.Split(v, ",") }

// wordList splits a list whose entries are single words (scopes, algorithms): commas or spaces.
func wordList(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
}

// authKeys returns the keys of sprint 4.2, in the order of the fields of Config.
func authKeys() []keyDef {
	return []keyDef{
		serveDef("auth.public_origin", "URL", "", "Origin that browsers use to reach the administration API: https://host[:port], or http:// with a loopback host; required with auth.oidc.enabled",
			setString(func(c *Config) *string { return &c.Auth.PublicOrigin })),
		def("auth.local.enabled", "boolean", "true", "Allow sign-in with a local account; false is refused unless auth.oidc.enabled",
			setBool(func(c *Config) *bool { return &c.Auth.Local.Enabled })),
		def("auth.local.password_min_length", "integer", "12", "Shortest password accepted, 8 to 128; below 12 warns",
			setInt(func(c *Config) *int { return &c.Auth.Local.PasswordMinLength })),
		def("auth.local.generated_password_ttl", "duration", "72h", "Time a generated password stays valid until it is changed; 1h to 30d",
			setDuration(func(c *Config) *Duration { return &c.Auth.Local.GeneratedPasswordTTL })),
		serveDef("auth.local.max_failures", "integer", "10", "Failed sign-ins of one username in auth.local.failure_window that lock it; 3 to 100",
			setInt(func(c *Config) *int { return &c.Auth.Local.MaxFailures })),
		serveDef("auth.local.failure_window", "duration", "15m", "Window in which failed sign-ins are counted; 1m to 24h",
			setDuration(func(c *Config) *Duration { return &c.Auth.Local.FailureWindow })),
		serveDef("auth.local.lock_duration", "duration", "15m", "Time a username stays locked; 1m to 24h",
			setDuration(func(c *Config) *Duration { return &c.Auth.Local.LockDuration })),
		serveDef("auth.local.source_max_failures", "integer", "50", "Failed sign-ins from one source address prefix in the window that delay it; at least auth.local.max_failures",
			setInt(func(c *Config) *int { return &c.Auth.Local.SourceMaxFailures })),
		def("auth.local.argon2.memory_kib", "integer", "65536", "Argon2id memory cost of new password hashes, in KiB",
			setInt(func(c *Config) *int { return &c.Auth.Local.Argon2.MemoryKiB })),
		def("auth.local.argon2.iterations", "integer", "3", "Argon2id time cost of new password hashes",
			setInt(func(c *Config) *int { return &c.Auth.Local.Argon2.Iterations })),
		def("auth.local.argon2.parallelism", "integer", "1", "Argon2id parallelism of new password hashes",
			setInt(func(c *Config) *int { return &c.Auth.Local.Argon2.Parallelism })),
		serveDef("auth.local.max_concurrent_hashes", "integer", "4", "Password hashes computed at the same time; more are answered 503; 1 to 64",
			setInt(func(c *Config) *int { return &c.Auth.Local.MaxConcurrentHashes })),
		serveDef("auth.session.idle_timeout", "duration", "30m", "Time without use after which a session ends; 1m to 24h, at most auth.session.absolute_lifetime",
			setDuration(func(c *Config) *Duration { return &c.Auth.Session.IdleTimeout })),
		serveDef("auth.session.absolute_lifetime", "duration", "12h", "Longest life of a session; at most 168h",
			setDuration(func(c *Config) *Duration { return &c.Auth.Session.AbsoluteLifetime })),
		serveDef("auth.session.max_per_user", "integer", "10", "Sessions a user may hold; the oldest end first; 1 to 100",
			setInt(func(c *Config) *int { return &c.Auth.Session.MaxPerUser })),
		serveDef("auth.oidc.enabled", "boolean", "false", "Allow sign-in through an OpenID Connect identity provider; needs issuer, client_id, client_secret_file and auth.public_origin",
			setBool(func(c *Config) *bool { return &c.Auth.OIDC.Enabled })),
		serveDef("auth.oidc.issuer", "URL", "", "Issuer of the identity provider; https",
			setString(func(c *Config) *string { return &c.Auth.OIDC.Issuer })),
		serveDef("auth.oidc.client_id", "string", "", "Client ID registered at the identity provider",
			setString(func(c *Config) *string { return &c.Auth.OIDC.ClientID })),
		serveDef("auth.oidc.client_secret_file", "path", "", "File with the client secret (at least 16 bytes); the value is a secret",
			setString(func(c *Config) *string { return &c.Auth.OIDC.ClientSecretFile })),
		serveDef("auth.oidc.client_auth", "basic, post", "basic", "How the client authenticates at the token endpoint",
			setString(func(c *Config) *string { return &c.Auth.OIDC.ClientAuth })),
		serveDef("auth.oidc.scopes", "list", "openid profile email", "Scopes requested, separated by commas in the environment; contains openid",
			func(c *Config, v string) error { c.Auth.OIDC.Scopes = wordList(v); return nil }),
		serveDef("auth.oidc.algorithms", "list", "RS256 ES256", "Signature algorithms accepted for the ID token, separated by commas in the environment",
			func(c *Config, v string) error { c.Auth.OIDC.Algorithms = wordList(v); return nil }),
		serveDef("auth.oidc.display_name", "string", "Single sign-on", "Name of the identity provider shown by the sign-in page",
			setString(func(c *Config) *string { return &c.Auth.OIDC.DisplayName })),
		serveDef("auth.oidc.name_claim", "string", "name", "Claim of the ID token that gives the display name",
			setString(func(c *Config) *string { return &c.Auth.OIDC.NameClaim })),
		serveDef("auth.oidc.email_claim", "string", "email", "Claim of the ID token that gives the e-mail address",
			setString(func(c *Config) *string { return &c.Auth.OIDC.EmailClaim })),
		serveDef("auth.oidc.groups_claim", "string", "groups", "Claim of the ID token that gives the groups; a dotted path of at most four members",
			setString(func(c *Config) *string { return &c.Auth.OIDC.GroupsClaim })),
		serveDef("auth.oidc.group_mappings", "list", "", "Rules <target>=<group>, separated by commas in the environment; the target is platform:<role> or tenant:<tenant name>:<role>",
			func(c *Config, v string) error { c.Auth.OIDC.GroupMappings = listValue(v); return nil }),
		serveDef("auth.oidc.auto_provision", "boolean", "false", "Create a user at the first sign-in of an unknown subject that the group rules give a role; refused without auth.oidc.group_mappings",
			setBool(func(c *Config) *bool { return &c.Auth.OIDC.AutoProvision })),
		serveDef("auth.oidc.private_hosts", "list", "", "Host names, separated by commas in the environment, whose URLs may resolve to private addresses, as tokens.private_hosts",
			func(c *Config, v string) error { c.Auth.OIDC.PrivateHosts = listValue(v); return nil }),
		serveDef("auth.oidc.post_sign_in_path", "path", "/", "Where the browser goes after sign-in through the identity provider: starts with one /, no //, no \\, no control character",
			setString(func(c *Config) *string { return &c.Auth.OIDC.PostSignInPath })),
		serveDef("idempotency.retention", "duration", "24h", "Time an idempotency key is kept; 1h to 7d",
			setDuration(func(c *Config) *Duration { return &c.Idempotency.Retention })),
	}
}

// loadOIDCSecret reads the client secret of the identity provider. No message quotes content.
func loadOIDCSecret(c *Config) []Problem {
	const key = "auth.oidc.client_secret_file"
	if c.Auth.OIDC.ClientSecretFile == "" {
		return []Problem{{Key: key, Message: "must be set when auth.oidc.enabled is true"}}
	}
	s, err := secret.ReadFile(c.Auth.OIDC.ClientSecretFile)
	if err != nil {
		return []Problem{{Key: key, Message: err.Error()}}
	}
	c.Auth.OIDC.ClientSecret = s
	return nil
}
