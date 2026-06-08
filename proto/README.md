# Protos

Regenerate Go stubs after editing `.proto`:

```sh
protoc --proto_path=proto \
  --go_out=. --go_opt=module=go.klarlabs.de/rolloffs \
  --go-grpc_out=. --go-grpc_opt=module=go.klarlabs.de/rolloffs \
  proto/rolloffs/v1/rolloffs.proto
```

Generated code lives in `internal/grpcapi/rolloffsv1/` and is checked in.
