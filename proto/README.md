# Protos

Regenerate Go stubs after editing `.proto`:

```sh
protoc --proto_path=proto \
  --go_out=. --go_opt=module=go.klarlabs.de/rollops \
  --go-grpc_out=. --go-grpc_opt=module=go.klarlabs.de/rollops \
  proto/rollops/v1/rollops.proto
```

Generated code lives in `internal/grpcapi/rollopsv1/` and is checked in.
