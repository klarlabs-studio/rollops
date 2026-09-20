# Protos

Two surfaces, two packages. `rollops.v1` is the engine's daemon transport;
`rollops.v2` is the gRPC projection of `internal/api/v2`, whose semantics are
settled in that package and shared with HTTP, MCP and the CLI (§23.1). A v2
package rather than a mutated v1, because a proto is a public contract (§24).

Regenerate Go stubs after editing a `.proto`:

```sh
protoc --proto_path=proto \
  --go_out=. --go_opt=module=go.klarlabs.de/rollops \
  --go-grpc_out=. --go-grpc_opt=module=go.klarlabs.de/rollops \
  proto/rollops/v1/rollops.proto proto/rollops/v2/rollops.proto
```

Generated code is checked in, beside the transport it belongs to:

| Proto                            | Stubs                                |
| -------------------------------- | ------------------------------------ |
| `proto/rollops/v1/rollops.proto` | `internal/grpcapi/rollopsv1/`        |
| `proto/rollops/v2/rollops.proto` | `internal/api/v2/grpcapi/rollopsv2/` |

v2's enums are closed sets the domain publishes — `deployment.Statuses()`,
`environment.Kinds()` and the rest. A test holds each proto enum against its
vocabulary, so a value added to the domain and forgotten here fails the build
rather than reaching a caller as UNSPECIFIED.
