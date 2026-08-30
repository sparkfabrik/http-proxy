## 1. Failing tests first

- [x] 1.1 Add a test asserting `generate-mkcert` does not restart the proxy and that the hostname is served with the new certificate.
- [x] 1.2 Add a test asserting `remove-cert` does not restart the proxy and that the removed certificate stops being served.
- [x] 1.3 Add a test asserting the scan writes an empty certificate list when no certificates remain, rather than leaving the previous file.
- [x] 1.4 Add a test asserting both commands succeed, report no error, and do not start the proxy when it is not running.
- [x] 1.5 Add a test asserting neither command's output says the proxy is being restarted.
- [x] 1.6 Add a test asserting nothing is sent to the container when the guard is absent from the entrypoint.
- [x] 1.7 Confirm each test fails against the current code, and for the intended reason.

## 2. Container

- [x] 2.1 Add the `--tls-only` guard to `build/traefik/entrypoint.sh`, after the function definitions and before `write_proxy_declaration`.
- [x] 2.2 Replace the early return in `generate_tls_config` for the no-certificates case with writing the header and an empty certificate list.
- [x] 2.3 Verify the normal start path is unchanged, including the declaration and peer-config cleanup.

## 3. CLI

- [x] 3.1 Add a helper that runs the scan: probe for the guard, run it when present, skip quietly when the proxy is not running or the guard is absent.
- [x] 3.2 Replace `dc_cmd restart traefik` in `generate_mkcert` (line 669) with that helper.
- [x] 3.3 Replace `dc_cmd restart traefik` in `remove_cert` (line 808) with that helper.
- [x] 3.4 Rewrite both messages so they describe applying the certificate, and say when it will apply if the scan was skipped.
- [x] 3.5 Leave `dc_cmd restart` at line 1708 alone; it is the `restart` command.

## 4. Documentation

- [x] 4.1 Update the note at `README.md:553` telling users to run `docker compose restart` after generating a certificate manually.
- [x] 4.2 Check the certificate sections of `README.md` for any other claim that a restart is needed.
- [x] 4.3 Add a CHANGELOG entry under Changed and Fixed as applicable.

## 5. Verification

- [ ] 5.1 Run the test suite and confirm the new tests pass.
- [x] 5.2 Run shellcheck on `bin/spark-http-proxy` and `build/traefik/entrypoint.sh`.
- [x] 5.3 Drive it as a user does on a real machine: generate a certificate, regenerate it, remove it, remove the last one, and with the proxy stopped.
- [x] 5.4 Run the `simplify` pass over the diff.
