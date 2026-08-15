# Heterogeneous demo

One daemon, two targets — the vision in YAML. Kubernetes is a target, not the
universe. Plan both with the same agent loop ([agent-operator](../../docs/agent-operator.md)).

```sh
bin/rollops doctor examples/hetero/ssh.yaml
bin/rollops doctor examples/hetero/kubernetes.yaml
bin/rollops plan examples/hetero/ssh.yaml
bin/rollops plan examples/hetero/kubernetes.yaml
```

Placeholders only; point `host` / `context` at your lab.
