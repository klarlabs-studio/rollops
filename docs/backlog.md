
## Target plugin process lifecycle + distribution

Run third-party target plugins as real subprocesses: launch the plugin binary, verify the handshake (protocol version + cookie) over a simple stdout line, dial its gRPC server on a unix socket, adapt it into pkg/target.Target, and tear the process down cleanly after the operation. Verify plugin binary integrity (sha256 pin in the target spec) before exec. Wire a "plugin" target kind into the registry/config schema so a plugin-backed target is declared in rollops.yaml like any other target. Document authoring + packaging.

---
