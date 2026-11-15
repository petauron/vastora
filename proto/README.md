# Vastora Agent control protocol

`vastora/agent/v1/control.proto` defines the Center/Agent gRPC boundary for
issue #1112. Agent and Glivela identities, authorization and task stores remain
separate. The stream is initiated by the Agent; an assignment is not proof of
execution. Existing task authorization, attempt fencing and encrypted envelopes
remain mandatory. Domain documents use bounded JSON bytes to preserve their
existing validation and numeric semantics; gRPC method and identity fields are
explicit rather than allowing arbitrary paths or remote commands.

The implementation connects the Agent control service and Caddy HTTP/2 upstream
routing in this workspace. Existing verification, safe version coordination and
rollout remain unfinished; generated definitions and source changes are not
evidence of a usable deployment.

Generated bindings are checked in under `internal/agentpb`. Generate them with
`protoc` 36.2, `protoc-gen-go` v1.36.12 and `protoc-gen-go-grpc` v1.6.2:

```sh
protoc --proto_path=proto --go_out=. --go_opt=module=github.com/petauron/vastora \
  --go-grpc_out=. --go-grpc_opt=module=github.com/petauron/vastora \
  proto/vastora/agent/v1/control.proto
```
