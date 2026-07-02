# Hearth

Private, invite-only, donation-funded social app for close connections. See
[Hearth_Technical_Plan.md](Hearth_Technical_Plan.md) and
[Hearth_Build_and_Test_Plan.md](Hearth_Build_and_Test_Plan.md) for the full
spec. See [CLAUDE.md](CLAUDE.md) for architectural rules and conventions.

## Status

Phase 0 — Foundation. Signup, email verification, login/logout, password
reset, profile view/edit, and soft account deletion are implemented. The
photo-upload field is stubbed pending R2 credentials.

## Local development

```sh
cp .env.example .env
go mod tidy
make run
```

The server listens on `:8080`. In dev mode the email sender prints every
"sent" message to stdout — copy the verification/reset link out of the log
line to complete the flow.

## Layout

See `Appendix A` of the Build & Test Plan. The actual tree mirrors it.

## Deploying

`Dockerfile` produces a static distroless image. `fly.toml` is a Fly.io
template. Litestream config in `litestream.yml`.

## Contributing

Hearth is a personal project shared as reference. You're welcome to fork and
adapt it under the license below, but I'm not accepting pull requests or
feature requests.

## License

Licensed under the GNU Affero General Public License v3.0 — see
[LICENSE](LICENSE). If you run a modified version as a network service, AGPL
§13 requires you to offer its source to your users.
