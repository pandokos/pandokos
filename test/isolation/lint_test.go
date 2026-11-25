package isolation

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/dbtest"
)

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return c
}

// TestSchemaLint is T-140: the real migrations produce a schema with zero findings. This test
// is the build gate of exit criterion 1: a table without a policy fails the build.
func TestSchemaLint(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	findings, err := Lint(ctx(t), d.Super(t), DefaultSpec())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Errorf("%s %s: %s", f.Rule, f.Object, f.Detail)
	}
}

// TestDefaultSpecMatchesDesign is T-142 (changed in sprint 2.1 and 2.2, T-390): the spec is the one of sprint 1.3,
// sections 5.10 and 6.1, with the vault tables, the metadata view and the pin of sprint 2.1,
// sections 5.3 and 6.
func TestDefaultSpecMatchesDesign(t *testing.T) {
	t.Parallel()
	want := Spec{
		Global: map[string]GlobalTable{
			"schema_migration":            {Reason: "describes the installation, holds no tenant information"},
			"tenant":                      {Reason: "the list of tenants; protected by its own policies", Restricted: true},
			"virtual_key_directory":       {Reason: "Key ID to tenant, for the lookup function; no runtime role has any privilege on it"},
			"platform_key":                {Reason: "Credentials of the platform; protected by its own policies", Restricted: true},
			"platform_audit_event":        {Reason: "Audit log of the platform; protected by its own policies", Restricted: true},
			"platform_audit_chain":        {Reason: "the head and anchor of the platform's audit chain; read by the admin role in platform scope, written by the seal and retention functions only", Restricted: true},
			"kek_version":                 {Reason: "identifiers of key-encryption key versions; describes the installation, holds no key and no tenant information"},
			"provider":                    {Reason: "providers of the catalogue; read by every runtime role, written in platform scope", Restricted: true},
			"provider_endpoint":           {Reason: "endpoints of providers; read by the gateway and the admin role, written in platform scope", Restricted: true},
			"model":                       {Reason: "models of the catalogue; read by every runtime role, written in platform scope", Restricted: true},
			"model_tenant_grant":          {Reason: "which tenants a model is granted to; a tenant's session sees its own rows; protected by its own policies", Restricted: true},
			"price_import":                {Reason: "runs of the price importer; platform scope only", Restricted: true},
			"price_version":               {Reason: "price versions; read by every runtime role, inserted in platform scope", Restricted: true},
			"price_rate":                  {Reason: "rates of price versions; read by every runtime role, inserted in platform scope", Restricted: true},
			"platform_issuer":             {Reason: "platform-level token issuers; read by the gateway and the admin role, written in platform scope", Restricted: true},
			"platform_issuer_tenant":      {Reason: "the tenants a platform-level token issuer may act for; a tenant's session sees its own rows, the platform scope all of them; protected by its own policies", Restricted: true},
			"issuer_directory":            {Reason: "which registration an issuer and audience select, for the lookup function; no runtime role has any privilege on it"},
			"platform_limit_rule":         {Reason: "ceilings the platform sets for tenants; a tenant's session sees the defaults and its own rows, the platform scope all of them; protected by its own policies", Restricted: true},
			"app_user":                    {Reason: "people; the platform scope sees them all, a tenant's session the users it manages and its members; protected by its own policies", Restricted: true},
			"local_account":               {Reason: "usernames and password hashes of local accounts; visible as app_user is; protected by its own policies", Restricted: true},
			"user_identity":               {Reason: "issuer and subject of federated users; platform scope only", Restricted: true},
			"user_session":                {Reason: "sessions of users; the platform scope sees them all, a tenant's session those of the users it manages; protected by its own policies", Restricted: true},
			"platform_role_assignment":    {Reason: "platform roles of users; platform scope only", Restricted: true},
			"signin_throttle":             {Reason: "failed sign-ins per username and source; platform scope only", Restricted: true},
			"oidc_login":                  {Reason: "sign-in transactions of the identity provider; platform scope only", Restricted: true},
			"idempotency_key":             {Reason: "idempotency keys of create operations; a tenant's session sees its own, the platform scope those of the platform; protected by its own policies", Restricted: true},
			"support_action":              {Reason: "Support actions of the platform inside tenants; protected by its own policies", Restricted: true},
			"tenant_deletion":             {Reason: "the request and the progress of the deletion of a tenant; platform scope only; written by the erasure function", Restricted: true},
			"event_destination_directory": {Reason: "which tenants have destinations, for the claim of the relay; identifiers only; no runtime role has any privilege on it"},
			"platform_outbox_stream":      {Reason: "the sequence of the platform's event log; written by the functions that append events; no runtime role has any privilege on it"},
			"platform_outbox_event":       {Reason: "the platform's events (tenant deletion, key versions); read by the admin role in platform scope, never changed and not removed; protected by its own policies", Restricted: true},
			"tenant_closed":               {Reason: "the tenants that take no new row; a session reads its own tenant's row, the platform scope all; written by the erasure function", Restricted: true},
			"membership_directory":        {Reason: "which tenants a user is a member of, for the lookup function; no runtime role has any privilege on it"},
		},
		Grants: map[string]map[string][]string{
			"schema_migration": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"SELECT"},
				"pandokos_worker":  {"SELECT"},
			},
			"kek_version": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"SELECT"},
				"pandokos_worker":  {"SELECT"},
			},
			"tenant_data_key": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"INSERT(kek_version,tenant_id,version,wrapped_key)", "SELECT"},
				"pandokos_worker":  {"SELECT", "UPDATE(kek_version,wrapped_key)"},
			},
			"tenant_closed": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"SELECT"},
				"pandokos_worker":  {"SELECT"},
			},
			"tenant_deletion": {
				"pandokos_admin": {
					"DELETE",
					"INSERT(not_before,reason,request_id,requested_by_id,requested_by_name,requested_by_type,tenant_id)",
					"SELECT(audit,completed_at,completion_seq,external_reference,kek_versions,not_before,passes,reason,removed,request_id,requested_at,requested_by_id,requested_by_name,requested_by_type,settle_at,started_at,state,step,tenant_id)",
				},
				"pandokos_worker": {"SELECT(kek_versions,not_before,request_id,started_at,state,step,tenant_id)"},
			},
			"provider_credential": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {
					"DELETE",
					"INSERT(description,expires_at,fingerprint,hint,id,is_default,key_version,label,provider,secret,tenant_id,validated_at)",
					"SELECT(created_at,description,expires_at,fingerprint,hint,id,is_default,key_version,label,provider,rotated_at,state,state_changed_at,state_changed_by,state_reason,tenant_id,updated_at,validated_at)",
					"UPDATE(description,expires_at,fingerprint,hint,is_default,key_version,label,secret,state,state_changed_by,state_reason,validated_at)",
				},
			},
			"provider_credential_metadata": {
				"pandokos_admin": {"SELECT"},
			},
			"tenant": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"INSERT(display_name,external_reference,name,time_zone)", "SELECT", "UPDATE(active_currency,active_currency_effective_from,active_currency_previous,display_name,external_reference,name,state,time_zone,time_zone_effective_from,time_zone_previous)"},
				"pandokos_worker":  {"SELECT"},
			},
			"service_account": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {
					"INSERT(description,name,state,tenant_id,valid_from,valid_until)",
					"SELECT",
					"UPDATE(description,name,state,valid_from,valid_until)",
				},
			},
			"role_assignment": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"DELETE", "INSERT(role,service_account_id,tenant_id)", "SELECT"},
			},
			"virtual_key": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {
					"INSERT(credential_id,description,expires_at,key_id,models,scopes,secret_hash,service_account_id,tenant_id)",
					"SELECT",
					"UPDATE(revoked_at)",
				},
			},
			"platform_key": {
				"pandokos_admin": {
					"INSERT(bootstrap,description,expires_at,key_id,role,secret_hash)",
					"SELECT",
					"UPDATE(revoked_at)",
				},
			},
			"audit_event": {
				"pandokos_admin": {"INSERT(action,actor_id,actor_name,actor_type,detail,key_id,outcome,reason,request_id,resource_id,resource_type,support_action_id,tenant_id)", "SELECT"},
			},
			"audit_chain": {
				"pandokos_admin": {"SELECT"},
			},
			"platform_audit_chain": {
				"pandokos_admin": {"SELECT"},
			},
			"usage_rollup": {
				"pandokos_admin":  {"SELECT"},
				"pandokos_worker": {"DELETE", "INSERT", "SELECT", "UPDATE"},
			},
			"usage_rollup_state": {
				"pandokos_admin":  {"SELECT"},
				"pandokos_worker": {"INSERT(tenant_id,watermark)", "SELECT", "UPDATE(updated_at,watermark)"},
			},
			"metadata_key_policy": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"DELETE", "INSERT(keys,tenant_id)", "SELECT", "UPDATE(keys)"},
			},
			"tenant_role": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"DELETE", "INSERT(description,name,permissions,tenant_id)", "SELECT", "UPDATE(description,permissions,state)"},
			},
			"support_action": {
				"pandokos_admin": {"INSERT(actor_id,actor_name,actor_type,expires_at,key_id,permissions,reason,request_id,tenant_id)", "SELECT", "UPDATE(close_reason,closed_at,closed_by,closed_by_id)"},
			},
			"platform_audit_event": {
				"pandokos_admin":  {"INSERT(action,actor_id,actor_name,actor_type,detail,key_id,outcome,reason,request_id,resource_id,resource_type)", "SELECT"},
				"pandokos_worker": {"INSERT(action,actor_id,actor_name,actor_type,detail,key_id,outcome,reason,request_id,resource_id,resource_type)"},
			},
			"provider": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {
					"DELETE",
					"INSERT(api_family,dataset_provider,description,display_name,enabled,name,stream_usage_option)",
					"SELECT",
					"UPDATE(dataset_provider,description,display_name,enabled,stream_usage_option)",
				},
				"pandokos_worker": {"SELECT"},
			},
			"provider_endpoint": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {
					"DELETE",
					"INSERT(base_url,description,enabled,is_default,name,private,provider_id,region)",
					"SELECT",
					"UPDATE(base_url,description,enabled,is_default,private,region)",
				},
			},
			"model": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {
					"DELETE",
					"INSERT(availability,capabilities,context_window,default_output_tokens,description,display_name,max_output_tokens,name,provider_id,status,upstream_id)",
					"SELECT",
					"UPDATE(availability,capabilities,context_window,default_output_tokens,description,display_name,max_output_tokens,price_override,status,upstream_id)",
				},
				"pandokos_worker": {"SELECT"},
			},
			"model_tenant_grant": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"DELETE", "INSERT(model_id,tenant_id)", "SELECT"},
			},
			"limit_rule": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {"DELETE", "INSERT(currency,enforce,limit_value,metric,model_id,provider_id,service_account_id,subject_kind,subject_mapping_id,tenant_id,thresholds,virtual_key_id)",
					"SELECT", "UPDATE(enforce,limit_value,thresholds)"},
			},
			"platform_limit_rule": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {"DELETE", "INSERT(currency,enforce,limit_value,metric,model_id,provider_id,tenant_id,thresholds)",
					"SELECT", "UPDATE(enforce,limit_value,thresholds)"},
			},
			"app_user": {
				"pandokos_admin": {"INSERT(display_name,email,sign_in,tenant_id)", "SELECT", "UPDATE(display_name,email,state)"},
			},
			"membership": {
				"pandokos_admin": {"DELETE", "INSERT(source,tenant_id,user_id)", "SELECT", "UPDATE(source)"},
			},
			"membership_role": {
				"pandokos_admin": {"DELETE", "INSERT(role,source,tenant_id,user_id)", "SELECT"},
			},
			"local_account": {
				"pandokos_admin": {"INSERT(must_change,password_expires_at,password_hash,user_id,username)", "SELECT", "UPDATE(must_change,password_changed_at,password_expires_at,password_hash)"},
			},
			"user_identity": {
				"pandokos_admin": {"INSERT(issuer,subject,user_id)", "SELECT"},
			},
			"user_session": {
				"pandokos_admin": {"DELETE", "INSERT(expires_at,method,token_hash,user_id)", "SELECT", "UPDATE(last_used_at)"},
			},
			"platform_role_assignment": {
				"pandokos_admin": {"DELETE", "INSERT(role,source,user_id)", "SELECT"},
			},
			"signin_throttle": {
				"pandokos_admin": {"DELETE", "INSERT(failures,key,kind,last_failure_at,locked_until,window_started_at)", "SELECT", "UPDATE(failures,last_failure_at,locked_until,window_started_at)"},
			},
			"oidc_login": {
				"pandokos_admin": {"DELETE", "INSERT(expires_at,state_hash)", "SELECT"},
			},
			"idempotency_key": {
				"pandokos_admin": {"DELETE", "INSERT(expires_at,key,locked_until,operation,principal_id,principal_kind,request_hash,salt,tenant_id)", "SELECT",
					"UPDATE(attempt,committed_at,completed_at,locked_until,resource_id,resource_type,response_body,response_status)"},
			},
			"outbox_stream": {
				"pandokos_admin":  {"SELECT"},
				"pandokos_worker": {"SELECT"},
			},
			"outbox_event": {
				"pandokos_admin":  {"SELECT"},
				"pandokos_worker": {"SELECT"},
			},
			"event_destination": {
				"pandokos_admin": {
					"DELETE",
					"INSERT(description,enabled,event_types,id,name,secret,secret_key_version,tenant_id,url)",
					"SELECT(created_at,cursor_seq,description,enabled,event_types,expired_events,failing_since,failures,id,last_error,last_failure_at,last_status,last_success_at,lease_token,lease_until,name,next_attempt_at,previous_secret_expires_at,previous_secret_key_version,secret_created_at,secret_key_version,tenant_id,test_completed_at,test_event_id,test_outcome,test_requested_at,test_status,updated_at,url)",
					"UPDATE(cursor_seq,description,enabled,event_types,failing_since,failures,lease_token,lease_until,name,next_attempt_at,previous_secret_expires_at,secret,secret_key_version,test_event_id,test_requested_at,updated_at,url)",
				},
				"pandokos_worker": {"SELECT", "UPDATE(cursor_seq,failing_since,failures,last_error,last_failure_at,last_status,last_success_at,lease_token,lease_until,next_attempt_at,test_completed_at,test_outcome,test_status)"},
			},
			"outbox_dead_letter": {
				"pandokos_admin":  {"SELECT", "UPDATE(replay_requested_at,state)"},
				"pandokos_worker": {"DELETE", "INSERT(attempts,destination_id,event_id,reason,seq,status,tenant_id,type)", "SELECT", "UPDATE(attempts,parked_at,reason,state,status)"},
			},
			"platform_outbox_event": {
				"pandokos_admin": {"SELECT"},
			},
			"limit_event": {
				"pandokos_gateway": {"INSERT(currency,kind,limit_value,metric,model,observed,percent,period_end,period_start,provider,rule_id,rule_level,subject_id,subject_kind,tenant_id)"},
				"pandokos_admin":   {"SELECT"},
			},
			"tenant_model_restriction": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"DELETE", "INSERT(models,tenant_id)", "SELECT", "UPDATE(models)"},
			},
			"price_import": {
				"pandokos_admin":  {"INSERT(actor_id,actor_type,dataset_format,digest,error_class,models_seen,outcome,overridden,report,skipped,source,source_ref,started_at,unchanged,unmatched,versions_created)", "SELECT"},
				"pandokos_worker": {"INSERT(actor_id,actor_type,dataset_format,digest,error_class,models_seen,outcome,overridden,report,skipped,source,source_ref,started_at,unchanged,unmatched,versions_created)", "SELECT"},
			},
			"price_version": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"DELETE", "INSERT(currency,import_id,model_id,note,source,valid_from)", "SELECT"},
				"pandokos_worker":  {"INSERT(currency,import_id,model_id,note,source,valid_from)", "SELECT"},
			},
			"tenant_price_version": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"DELETE", "INSERT(currency,kind,model_id,note,tenant_id,valid_from)", "SELECT"},
			},
			"tenant_price_rate": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"INSERT(category,price_version_id,rate,tenant_id,threshold)", "SELECT"},
			},
			"usage_record": {
				"pandokos_gateway": {"INSERT(api_family,cache_read_tokens,cache_write_tokens,cost,cost_fallbacks,cost_reason,cost_status,credential_id,currency,duration_ms,endpoint,error_code,estimated_input_tokens,estimated_output_tokens,id,input_source,input_tokens,key_id,limit_state,metadata,metadata_dropped,model,model_id,operation,other_units,outcome,output_source,output_tokens,price_source,price_version_id,principal_id,principal_kind,provider,provider_usage,reasoning_tokens,request_bytes,request_id,response_bytes,started_at,stream,tenant_id,trace_id,ttfb_ms,upstream_model,upstream_status)"},
				"pandokos_admin":   {"SELECT"},
				"pandokos_worker":  {"SELECT"},
			},
			"price_rate": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"INSERT(category,price_version_id,rate,threshold)", "SELECT"},
				"pandokos_worker":  {"INSERT(category,price_version_id,rate,threshold)", "SELECT"},
			},
			"trusted_issuer": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {
					"DELETE",
					"INSERT(algorithms,audience,description,enabled,issuer,jwks_uri,name,required_claims,subject_claim,tenant_id)",
					"SELECT",
					"UPDATE(algorithms,audience,description,enabled,issuer,jwks_uri,name,required_claims,subject_claim)",
				},
			},
			"platform_issuer": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {
					"DELETE",
					"INSERT(algorithms,audience,description,enabled,issuer,jwks_uri,name,required_claims,subject_claim,tenant_claim)",
					"SELECT",
					"UPDATE(algorithms,audience,description,enabled,issuer,jwks_uri,name,required_claims,subject_claim,tenant_claim)",
				},
			},
			"platform_issuer_tenant": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"DELETE", "INSERT(claim_value,issuer_id,tenant_id)", "SELECT", "UPDATE(claim_value)"},
			},
			"subject_mapping": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"DELETE", "INSERT(description,platform_issuer_id,service_account_id,subject,tenant_id,trusted_issuer_id)", "SELECT"},
			},
		},
		Fixtures: map[string]Fixture{
			"service_account": {
				Insert: `INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, 'sa-' || substr(md5(random()::text), 1, 12)) RETURNING id::text`,
				Update: `description = 'changed'`,
			},
			"role_assignment": {
				Insert: `WITH sa AS (INSERT INTO pandokos.service_account (tenant_id, name)
            VALUES ($1::uuid, 'sa-' || substr(md5(random()::text), 1, 12)) RETURNING id)
INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role)
SELECT $1::uuid, sa.id, 'inference' FROM sa RETURNING id::text`,
				Update: `role = 'changed'`,
			},
			"virtual_key": {
				Insert: `WITH sa AS (INSERT INTO pandokos.service_account (tenant_id, name)
            VALUES ($1::uuid, 'sa-' || substr(md5(random()::text), 1, 12)) RETURNING id)
INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash)
SELECT $1::uuid, sa.id, 't' || translate(substr(md5(random()::text), 1, 16), '0189', 'ghij'),
       sha256(random()::text::bytea) FROM sa RETURNING id::text`,
				Update: `description = 'changed'`,
			},
			"tenant_data_key": {
				Insert: `INSERT INTO pandokos.tenant_data_key (tenant_id, kek_version, wrapped_key)
SELECT $1::uuid, v.id, decode(repeat('00', 61), 'hex') FROM pandokos.kek_version v WHERE v.state = 'active' RETURNING id::text`,
				Update: `wrapped_key = decode(repeat('11', 61), 'hex')`,
			},
			"provider_credential": {
				Insert: `INSERT INTO pandokos.provider_credential (tenant_id, provider, label, key_version, secret, fingerprint)
VALUES ($1::uuid, 'openai', 'c-' || substr(md5(random()::text), 1, 12), 1, decode(repeat('00', 61), 'hex'), md5(random()::text)) RETURNING id::text`,
				Update: `description = 'changed'`,
			},
			"tenant_role": {
				Insert: `INSERT INTO pandokos.tenant_role (tenant_id, name, permissions) VALUES ($1::uuid, 'fixture-' || substr(md5(random()::text), 1, 12), ARRAY['member:read']) RETURNING id::text`,
				Update: `description = 'changed'`,
			},
			"audit_event": {
				Insert: `INSERT INTO pandokos.audit_event (tenant_id, actor_type, action, resource_type, outcome)
VALUES ($1::uuid, 'platform', 'test.fixture', 'test', 'success') RETURNING id::text`,
				Update: `reason = 'changed'`,
			},
			"audit_chain": {
				Insert: `INSERT INTO pandokos.audit_chain (tenant_id) VALUES ($1::uuid) RETURNING tenant_id::text`,
				Key:    "tenant_id",
				Update: `anchor_seq = 1`,
			},
			"usage_rollup": {
				Insert: `INSERT INTO pandokos.usage_rollup (tenant_id, hour, principal_kind, principal_id, key_id, provider, model, model_id,
                                       currency, price_source, outcome, credential, requests, input_tokens, output_tokens,
                                       cache_read_tokens, cache_write_tokens, reasoning_tokens, estimated_input_tokens,
                                       estimated_output_tokens, cost, cost_known, cost_unknown)
VALUES ($1::uuid, date_bin('1 hour', now(), '2000-01-01 00:00:00+00'), '', '', '', '', '', '', '', '', '', true,
        1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0) RETURNING tenant_id::text`,
				Key:    "tenant_id",
				Update: `requests = 2`,
			},
			"usage_rollup_state": {
				Insert: `INSERT INTO pandokos.usage_rollup_state (tenant_id, watermark) VALUES ($1::uuid, now()) RETURNING tenant_id::text`,
				Key:    "tenant_id",
				Update: `watermark = now()`,
			},
			"metadata_key_policy": {
				Insert: `INSERT INTO pandokos.metadata_key_policy (tenant_id, keys) VALUES ($1::uuid, ARRAY['team']) RETURNING id::text`,
				Update: `keys = ARRAY['project']`,
			},
			"usage_record": {
				Insert: `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id,
                                   provider, api_family, operation, outcome, duration_ms, input_source,
                                   output_source, cost_status)
VALUES ($1, pandokos.uuid_v7(), now(), md5(random()::text), 'service_account', gen_random_uuid()::text,
        'openai', 'openai', 'chat.completions', 'ok', 1, 'none', 'none', 'none') RETURNING id::text`,
				Update: `model = 'changed'`,
			},
			"trusted_issuer": {
				Insert: `INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience)
VALUES ($1::uuid, 'ti-' || substr(md5(random()::text), 1, 12),
        'https://' || substr(md5(random()::text), 1, 16) || '.example.test', 'aud-' || $1::uuid::text) RETURNING id::text`,
				Update: `description = 'changed'`,
			},
			"subject_mapping": {
				Insert: `WITH sa AS (INSERT INTO pandokos.service_account (tenant_id, name)
            VALUES ($1::uuid, 'sa-' || substr(md5(random()::text), 1, 12)) RETURNING id),
     ti AS (INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience)
            VALUES ($1::uuid, 'ti-' || substr(md5(random()::text), 1, 12),
                    'https://' || substr(md5(random()::text), 1, 16) || '.example.test', 'aud-' || $1::uuid::text) RETURNING id)
INSERT INTO pandokos.subject_mapping (tenant_id, trusted_issuer_id, subject, service_account_id)
SELECT $1::uuid, ti.id, 'sub-' || substr(md5(random()::text), 1, 12), sa.id FROM sa, ti RETURNING id::text`,
				Update: `subject = 'changed'`,
			},
			"limit_rule": {
				Insert: `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, metric, limit_value)
VALUES ($1::uuid, 'tenant', 'requests_per_minute', 10) RETURNING id::text`,
				Update: `limit_value = 11`,
			},
			"limit_event": {
				Insert: `INSERT INTO pandokos.limit_event (tenant_id, kind, rule_id, rule_level, subject_kind, metric, period_start, period_end, percent, limit_value, observed)
VALUES ($1::uuid, 'threshold_reached', pandokos.uuid_v7(), 'tenant', 'tenant', 'tokens_per_day', date_trunc('day', now()), date_trunc('day', now()) + interval '1 day', 80, 10, 8) RETURNING id::text`,
				Update: `percent = 81`,
			},
			"tenant_price_version": {
				Insert: `WITH v AS (INSERT INTO pandokos.tenant_price_version (tenant_id, model_id, valid_from, currency)
           SELECT $1::uuid, m.id, now() + interval '1 hour' + random() * interval '1 hour', 'EUR'
             FROM pandokos.model m ORDER BY m.id LIMIT 1 RETURNING tenant_id, id),
     r AS (INSERT INTO pandokos.tenant_price_rate (tenant_id, price_version_id, category, rate)
           SELECT v.tenant_id, v.id, 'input', 1 FROM v RETURNING price_version_id)
SELECT r.price_version_id::text FROM r`,
				Update: `note = 'changed'`,
			},
			"membership": {
				Insert: `WITH u AS (INSERT INTO pandokos.app_user (sign_in, tenant_id) VALUES ('local', $1::uuid) RETURNING id)
INSERT INTO pandokos.membership (tenant_id, user_id) SELECT $1::uuid, u.id FROM u RETURNING id::text`,
				Update: `source = 'idp'`,
			},
			"membership_role": {
				Insert: `WITH u AS (INSERT INTO pandokos.app_user (sign_in, tenant_id) VALUES ('local', $1::uuid) RETURNING id),
     m AS (INSERT INTO pandokos.membership (tenant_id, user_id) SELECT $1::uuid, u.id FROM u RETURNING user_id)
INSERT INTO pandokos.membership_role (tenant_id, user_id, role) SELECT $1::uuid, m.user_id, 'tenant-admin' FROM m RETURNING id::text`,
				Update: `source = 'idp'`,
			},
			"outbox_stream": {
				Insert: `INSERT INTO pandokos.outbox_stream (tenant_id, last_seq) VALUES ($1::uuid, 1)
ON CONFLICT (tenant_id) DO UPDATE SET last_seq = pandokos.outbox_stream.last_seq + 1 RETURNING tenant_id::text`,
				Key:    "tenant_id",
				Update: `last_seq = 99`,
			},
			"outbox_event": {
				Insert: `INSERT INTO pandokos.outbox_event (tenant_id, seq, id, type, occurred_at, data)
SELECT $1::uuid, COALESCE(max(e.seq), 0) + 1, pandokos.uuid_v7(), 'io.pandokos.usage.recorded.v1', now(), '{}'::jsonb
  FROM pandokos.outbox_event e WHERE e.tenant_id = $1::uuid RETURNING id::text`,
				Update: `subject = 'changed'`,
			},
			"event_destination": {
				Insert: `INSERT INTO pandokos.event_destination (tenant_id, name, url, secret, secret_key_version)
VALUES ($1::uuid, 'ed-' || substr(md5(random()::text), 1, 12), 'https://hooks.example.test/in', decode(repeat('ab', 40), 'hex'), 1) RETURNING id::text`,
				Update: `description = 'changed'`,
			},
			"outbox_dead_letter": {
				Insert: `WITH d AS (INSERT INTO pandokos.event_destination (tenant_id, name, url, secret, secret_key_version)
           VALUES ($1::uuid, 'ed-' || substr(md5(random()::text), 1, 12), 'https://hooks.example.test/in', decode(repeat('ab', 40), 'hex'), 1) RETURNING id)
INSERT INTO pandokos.outbox_dead_letter (tenant_id, destination_id, seq, event_id, type, attempts, reason)
SELECT $1::uuid, d.id, 1, pandokos.uuid_v7(), 'io.pandokos.usage.recorded.v1', 1, 'status' FROM d RETURNING destination_id::text`,
				Key:    "destination_id",
				Update: `attempts = 2`,
			},
			"tenant_price_rate": {
				Insert: `WITH v AS (INSERT INTO pandokos.tenant_price_version (tenant_id, model_id, valid_from, currency)
           SELECT $1::uuid, m.id, now() + interval '1 hour' + random() * interval '1 hour', 'EUR'
             FROM pandokos.model m ORDER BY m.id LIMIT 1 RETURNING tenant_id, id),
     r AS (INSERT INTO pandokos.tenant_price_rate (tenant_id, price_version_id, category, rate)
           SELECT v.tenant_id, v.id, 'input', 1 FROM v RETURNING price_version_id)
SELECT r.price_version_id::text FROM r`,
				Key:    "price_version_id",
				Update: `rate = 2`,
			},
			"tenant_model_restriction": {
				Insert: `INSERT INTO pandokos.tenant_model_restriction (tenant_id, models) VALUES ($1::uuid, ARRAY['openai/gpt-4o']) RETURNING id::text`,
				Update: `models = ARRAY['openai/gpt-4o-mini']`,
			},
		},
		SecurityDefiner: []string{"virtual_key_lookup", "virtual_key_directory_add", "record_auth_failure", "ensure_usage_partitions",
			"issuer_lookup", "platform_issuer_tenant_lookup", "record_token_auth_failure", "issuer_directory_sync", "platform_issuer_mapping_count",
			"delete_platform_issuer_limit_rules", "record_unlimited_traffic",
			"audit_seal_tenant", "audit_seal_platform", "platform_retention", "tenant_retention",
			"membership_guard", "membership_directory_sync", "user_tenants", "platform_role_assignment_guard", "platform_user_lookup",
			"kek_activate", "kek_retire", "tenant_deletion_holds", "tenant_erasure_step", "deleted_tenant_retention",
			"usage_record_outbox", "limit_event_outbox", "tenant_deletion_outbox", "kek_version_outbox", "event_destination_directory_sync",
			"outbox_relay_claim", "outbox_relay_state", "tenant_outbox_retention"},
		Views:      []string{"provider_credential_metadata"},
		AppendOnly: []string{"audit_event", "platform_audit_event", "price_import", "usage_record", "limit_event", "outbox_event", "platform_outbox_event"},
	}
	got := DefaultSpec()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DefaultSpec() differs from the design:\n got %+v\nwant %+v", got, want)
	}
	if !reflect.DeepEqual(got.Views, []string{"provider_credential_metadata"}) {
		t.Errorf("Views = %v, want only provider_credential_metadata", got.Views)
	}
}

const (
	canonicalTable = `CREATE TABLE pandokos.zz (
    tenant_id uuid NOT NULL REFERENCES pandokos.tenant (id),
    id uuid NOT NULL DEFAULT pandokos.uuid_v7(),
    PRIMARY KEY (tenant_id, id)
);`
	enable = `ALTER TABLE pandokos.zz ENABLE ROW LEVEL SECURITY;`
	force  = `ALTER TABLE pandokos.zz FORCE ROW LEVEL SECURITY;`
	policy = `CREATE POLICY tenant_isolation ON pandokos.zz
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));`
	ok = enable + force + policy
)

// TestSchemaLintDetects is T-141: for each rule L-01 to L-19 a deliberately defective object makes
// it fire.
func TestSchemaLintDetects(t *testing.T) {
	t.Parallel()
	type row struct {
		name string
		ddl  string // run as pandokos_owner
		post string // run as the superuser afterwards
		spec func(*Spec)
		rule string // the rule that must fire; several rules separated by commas must all fire
		only bool   // the findings must be exactly one, with this rule, on pandokos.zz
	}
	rows := []row{
		{name: "owner", ddl: canonicalTable + ok, post: `ALTER TABLE pandokos.zz OWNER TO pandokos_admin`, rule: "L-01,L-15"},
		{name: "view", ddl: `CREATE VIEW pandokos.zz_v AS SELECT id FROM pandokos.service_account`, rule: "L-02"},
		{name: "view-without-security-invoker", ddl: `CREATE VIEW pandokos.zz_v AS SELECT 1 AS x`,
			spec: func(s *Spec) { s.Views = append(s.Views, "zz_v") }, rule: "L-19"},
		{name: "no-tenant-column", ddl: `CREATE TABLE pandokos.zz (id uuid PRIMARY KEY)`, rule: "L-03"},
		{name: "nullable-tenant", ddl: `CREATE TABLE pandokos.zz (
    tenant_id uuid REFERENCES pandokos.tenant (id),
    id uuid NOT NULL DEFAULT pandokos.uuid_v7(),
    PRIMARY KEY (id)
);` + ok, rule: "L-03"},
		{name: "no-tenant-fk", ddl: `CREATE TABLE pandokos.zz (
    tenant_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT pandokos.uuid_v7(),
    PRIMARY KEY (tenant_id, id)
);` + ok, rule: "L-04"},
		{name: "rls-not-enabled", ddl: canonicalTable, rule: "L-05"},
		{name: "rls-not-forced", ddl: canonicalTable + enable + policy, rule: "L-06"},
		{name: "no-policy", ddl: canonicalTable + enable + force, rule: "L-07"},
		{name: "policy-true", ddl: canonicalTable + enable + force + `CREATE POLICY tenant_isolation ON pandokos.zz USING (true) WITH CHECK (true);`, rule: "L-08"},
		{name: "policy-select-only", ddl: canonicalTable + enable + force + `CREATE POLICY tenant_isolation ON pandokos.zz FOR SELECT
    USING (tenant_id = (SELECT pandokos.current_tenant_id()));`, rule: "L-08"},
		{name: "policy-raw-setting", ddl: canonicalTable + enable + force + `CREATE POLICY tenant_isolation ON pandokos.zz
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);`, rule: "L-08"},
		{name: "policy-restricted-roles", ddl: canonicalTable + enable + force + `CREATE POLICY tenant_isolation ON pandokos.zz TO pandokos_admin
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));`, rule: "L-08"},
		{name: "extra-policy", ddl: canonicalTable + ok + `CREATE POLICY wide ON pandokos.zz FOR SELECT USING (true);`, rule: "L-09"},
		{name: "key-without-tenant", ddl: `CREATE TABLE pandokos.zz (
    tenant_id uuid NOT NULL REFERENCES pandokos.tenant (id),
    id uuid NOT NULL DEFAULT pandokos.uuid_v7(),
    PRIMARY KEY (id)
);` + ok, rule: "L-10"},
		{name: "fk-without-tenant", ddl: `ALTER TABLE pandokos.service_account ADD CONSTRAINT zz_u UNIQUE (id);
CREATE TABLE pandokos.zz (
    tenant_id uuid NOT NULL REFERENCES pandokos.tenant (id),
    id uuid NOT NULL DEFAULT pandokos.uuid_v7(),
    sa uuid REFERENCES pandokos.service_account (id),
    PRIMARY KEY (tenant_id, id)
);` + ok, rule: "L-11"},
		{name: "grant-truncate", ddl: `GRANT TRUNCATE ON pandokos.service_account TO pandokos_admin`, rule: "L-12"},
		{name: "grant-public", ddl: `GRANT SELECT ON pandokos.service_account TO PUBLIC`, rule: "L-12"},
		{name: "grant-gateway-write", ddl: `GRANT UPDATE ON pandokos.service_account TO pandokos_gateway`, rule: "L-12"},
		{name: "grant-column", ddl: `GRANT UPDATE (id) ON pandokos.service_account TO pandokos_admin`, rule: "L-12"},
		{name: "missing-grant", ddl: `REVOKE SELECT ON pandokos.tenant FROM pandokos_worker`, rule: "L-12"},
		{name: "tenant-not-forced", ddl: `ALTER TABLE pandokos.tenant NO FORCE ROW LEVEL SECURITY`, rule: "L-13"},
		{name: "security-definer", ddl: `CREATE FUNCTION pandokos.zz_f() RETURNS integer LANGUAGE sql SECURITY DEFINER RETURN 1`, rule: "L-14"},
		{name: "public-function", ddl: `CREATE FUNCTION pandokos.zz_g() RETURNS integer LANGUAGE sql RETURN 1;
GRANT EXECUTE ON FUNCTION pandokos.zz_g() TO PUBLIC;`, rule: "L-14"},
		{name: "runtime-create", post: `GRANT CREATE ON SCHEMA pandokos TO pandokos_worker`, rule: "L-15"},
		{name: "append-only-grant", ddl: `GRANT UPDATE ON pandokos.audit_event TO pandokos_admin`, rule: "L-17,L-12"},
		{name: "append-only-column-grant", ddl: `GRANT UPDATE (reason) ON pandokos.platform_audit_event TO pandokos_admin`, rule: "L-17,L-12"},
		{name: "append-only-missing-row-trigger", ddl: `DROP TRIGGER audit_event_append_only ON pandokos.audit_event`, rule: "L-17"},
		{name: "append-only-missing-truncate-trigger", ddl: `DROP TRIGGER platform_audit_event_no_truncate ON pandokos.platform_audit_event`, rule: "L-17"},
		{name: "append-only-disabled-trigger", ddl: `ALTER TABLE pandokos.audit_event DISABLE TRIGGER audit_event_append_only`, rule: "L-17"},
		{name: "append-only-wrong-function", ddl: `DROP TRIGGER audit_event_append_only ON pandokos.audit_event;
CREATE TRIGGER audit_event_append_only BEFORE UPDATE OR DELETE ON pandokos.audit_event FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();`, rule: "L-17"},
		{name: "definer-without-search-path", ddl: `ALTER FUNCTION pandokos.virtual_key_lookup(text) RESET search_path`, rule: "L-18"},
		{name: "definer-without-settings-off", ddl: `CREATE OR REPLACE FUNCTION pandokos.record_auth_failure(p_key_id text, p_reason text, p_request_id text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$ BEGIN INSERT INTO pandokos.audit_event (tenant_id, actor_type, key_id, action, resource_type, resource_id, outcome, request_id)
    VALUES ((SELECT pandokos.current_tenant_id()), 'anonymous', p_key_id, 'auth.failure', 'virtual_key', p_key_id, 'failure', p_request_id); END $$`, rule: "L-22"},
		{name: "definer-without-restore", ddl: `CREATE OR REPLACE FUNCTION pandokos.virtual_key_directory_add() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
BEGIN
    INSERT INTO pandokos.virtual_key_directory (key_id, tenant_id) VALUES (NEW.key_id, NEW.tenant_id);
    RETURN NULL;
END $$`, rule: "L-22"},
		{name: "guard-without-owner", ddl: `CREATE OR REPLACE FUNCTION pandokos.tenant_price_version_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp
AS $$ BEGIN
    IF TG_OP = 'DELETE' AND pg_catalog.current_setting('pandokos.retention', true) IS DISTINCT FROM 'on' THEN
        RAISE EXCEPTION 'no' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN OLD;
END $$`, rule: "L-22"},
		{name: "setting-policy-for-everybody", ddl: `CREATE POLICY zz_retention ON pandokos.service_account FOR SELECT USING (pg_catalog.current_setting('pandokos.retention', true) = 'on')`, rule: "L-22"},
		{name: "new-definer-without-settings-off", ddl: `CREATE FUNCTION pandokos.zz_f() RETURNS integer LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog, pg_temp RETURN 1`,
			spec: func(s *Spec) { s.SecurityDefiner = append(s.SecurityDefiner, "zz_f") }, rule: "L-22"},
		{name: "no-fixture", ddl: canonicalTable + ok + `CREATE TRIGGER zz_tenant_open BEFORE INSERT ON pandokos.zz FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();`, rule: "L-16", only: true},
		{name: "no-tenant-open-trigger", ddl: canonicalTable + ok, rule: "L-21"},
		{name: "stale-spec", spec: func(s *Spec) { s.Fixtures["gone"] = Fixture{Insert: "SELECT 1"} }, rule: "L-16"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			d := dbtest.NewMigrated(t)
			super := d.Super(t)
			if r.ddl != "" {
				if _, err := super.Exec(ctx(t), "SET ROLE pandokos_owner"); err != nil {
					t.Fatal(err)
				}
				if _, err := super.Exec(ctx(t), r.ddl); err != nil {
					t.Fatalf("ddl: %v", err)
				}
				if _, err := super.Exec(ctx(t), "RESET ROLE"); err != nil {
					t.Fatal(err)
				}
			}
			if r.post != "" {
				if _, err := super.Exec(ctx(t), r.post); err != nil {
					t.Fatalf("post: %v", err)
				}
			}
			spec := DefaultSpec()
			if r.spec != nil {
				r.spec(&spec)
			}
			findings, err := Lint(ctx(t), d.Super(t), spec)
			if err != nil {
				t.Fatal(err)
			}
			if r.only {
				if len(findings) != 1 || findings[0].Rule != r.rule || findings[0].Object != "pandokos.zz" {
					t.Fatalf("findings %+v, want exactly one %s on pandokos.zz", findings, r.rule)
				}
				return
			}
			for _, want := range strings.Split(r.rule, ",") {
				fired := false
				for _, f := range findings {
					if f.Rule == want {
						fired = true
					}
				}
				if !fired {
					t.Errorf("rule %s did not fire; findings: %+v", want, findings)
				}
			}
		})
	}
}
