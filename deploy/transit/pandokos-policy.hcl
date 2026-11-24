path "transit/encrypt/pandokos"       { capabilities = ["update"] }
path "transit/decrypt/pandokos"       { capabilities = ["update"] }
path "transit/hmac/pandokos/sha2-256" { capabilities = ["update"] }
path "transit/keys/pandokos"          { capabilities = ["read"] }
path "auth/token/lookup-self"         { capabilities = ["read"] }
path "auth/token/renew-self"          { capabilities = ["update"] }
