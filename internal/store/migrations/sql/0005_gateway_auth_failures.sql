-- 0005_gateway_auth_failures: the gateway records authentication failures of the inference
-- listener in the audit log of the tenant whose key was presented (sprint 3.1, section 5.10).
--
-- The gateway role gets no privilege on audit_event. It may execute one function, which writes
-- one kind of row, for the tenant of the session, with fixed columns. Outside a tenant session
-- the forced policy of audit_event refuses the row.
CREATE FUNCTION pandokos.record_auth_failure(p_key_id text, p_reason text, p_request_id text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF p_key_id !~ '^t[a-z2-7]{16}$' OR p_reason !~ '^[a-z_]{1,40}$' OR p_request_id !~ '^[0-9a-f]{32}$' THEN
        RAISE EXCEPTION 'invalid authentication failure record' USING ERRCODE = 'check_violation';
    END IF;
    INSERT INTO pandokos.audit_event (tenant_id, actor_type, key_id, action, resource_type, resource_id,
                                      outcome, request_id, detail)
    VALUES ((SELECT pandokos.current_tenant_id()), 'anonymous', p_key_id, 'auth.failure', 'virtual_key', p_key_id,
            'failure', p_request_id, pg_catalog.jsonb_build_object('reason', p_reason, 'listener', 'inference'));
END
$$;

REVOKE ALL ON FUNCTION pandokos.record_auth_failure(text, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.record_auth_failure(text, text, text) TO pandokos_gateway;
