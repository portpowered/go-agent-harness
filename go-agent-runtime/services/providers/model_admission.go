package providers

// ModelAdmissionOptions describes the normalization and presentation policy
// requested by a host edge. The canonical ModelAdmission method uses both
// trim flags and normalized model errors; legacy adapters may preserve the
// original model text in an unsupported-model error.
type ModelAdmissionOptions struct {
	TrimProvider         bool
	TrimModel            bool
	PreserveModelInError bool
}

// ModelAdmissionResolver is the optional provider-owned extension returned by
// the per-service Wire constructor. ModelAdmission remains the stable minimal
// contract used by ordinary hosts.
type ModelAdmissionResolver interface {
	ModelAdmission
	ValidateRealtimeModel(provider, model string, options ModelAdmissionOptions) error
	ResolveRealtimeModel(provider, model string, options ModelAdmissionOptions) (RealtimeModel, bool)
}
