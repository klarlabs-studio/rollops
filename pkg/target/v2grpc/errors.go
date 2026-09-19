package v2grpc

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "go.klarlabs.de/rollops/pkg/target/rollopstargetv2"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// kindToCode places a contract failure on the wire. The mapping is not
// injective in the other direction — several kinds would otherwise collapse
// onto codes.Internal — which is why the kind also travels as a status detail
// and the code is only the fallback.
var kindToCode = map[targetv2.Kind]codes.Code{
	targetv2.KindUnsupported: codes.Unimplemented,
	targetv2.KindInvalid:     codes.InvalidArgument,
	targetv2.KindNotFound:    codes.NotFound,
	targetv2.KindDenied:      codes.PermissionDenied,
	targetv2.KindConflict:    codes.Aborted,
	targetv2.KindUnavailable: codes.Unavailable,
	targetv2.KindTimeout:     codes.DeadlineExceeded,
	targetv2.KindCanceled:    codes.Canceled,
	targetv2.KindInternal:    codes.Internal,
}

// codeToKind reads a failure that carried no detail — an older plugin, or one
// written against the generated service directly. Every code the host has an
// opinion about is named; the rest are internal, which is the honest answer for
// "something went wrong and the far side did not say what".
var codeToKind = map[codes.Code]targetv2.Kind{
	codes.Unimplemented:    targetv2.KindUnsupported,
	codes.InvalidArgument:  targetv2.KindInvalid,
	codes.NotFound:         targetv2.KindNotFound,
	codes.PermissionDenied: targetv2.KindDenied,
	codes.Unauthenticated:  targetv2.KindDenied,
	codes.Aborted:          targetv2.KindConflict,
	codes.AlreadyExists:    targetv2.KindConflict,
	codes.Unavailable:      targetv2.KindUnavailable,
	codes.DeadlineExceeded: targetv2.KindTimeout,
	codes.Canceled:         targetv2.KindCanceled,
}

// toStatus turns a contract error into the error the server returns. The
// message is preserved as the status message and the structured part as a
// detail, so a host reads fields rather than prose.
func toStatus(err error) error {
	if err == nil {
		return nil
	}
	kind := targetv2.KindOf(err)
	code, ok := kindToCode[kind]
	if !ok {
		code = codes.Internal
	}

	detail := &pb.TargetError{Kind: string(kind)}
	var te *targetv2.Error
	if errors.As(err, &te) {
		detail.Op = te.Op
		detail.Capability = string(te.Capability)
		detail.IdempotencyKey = te.IdempotencyKey
	}

	st, attachErr := status.New(code, err.Error()).WithDetails(detail)
	if attachErr != nil {
		// The detail could not be marshalled, which leaves the code as the
		// only signal. Saying less is better than failing the call for a
		// reason the caller cannot act on.
		return status.Error(code, err.Error())
	}
	return st.Err()
}

// fromStatus turns a wire failure back into a contract error, preferring the
// detail the far side attached and falling back to the code.
func fromStatus(op string, err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return targetv2.Failf(targetv2.KindOf(err), op, err, "%v", err)
	}

	for _, d := range st.Details() {
		te, ok := d.(*pb.TargetError)
		if !ok {
			continue
		}
		remoteOp := te.GetOp()
		if remoteOp == "" {
			remoteOp = op
		}
		at := &targetv2.Error{
			Kind:           targetv2.Kind(te.GetKind()),
			Op:             remoteOp,
			Message:        st.Message(),
			Capability:     targetv2.Capability(te.GetCapability()),
			IdempotencyKey: te.GetIdempotencyKey(),
			Err:            err,
		}
		if at.Kind == "" {
			at.Kind = kindFromCode(st.Code())
		}
		return at
	}

	return &targetv2.Error{
		Kind:    kindFromCode(st.Code()),
		Op:      op,
		Message: st.Message(),
		Err:     err,
	}
}

func kindFromCode(c codes.Code) targetv2.Kind {
	if k, ok := codeToKind[c]; ok {
		return k
	}
	return targetv2.KindInternal
}
