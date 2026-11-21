package llm

// Service tiers an Options.ServiceTier names (protocol minor version 28). A tier is the client's
// request for how its request is served and billed. The vocabulary is OpenAI's, since it names
// the most tiers; a format with fewer says the same tier in its own words (see ServiceTier).
const (
	// ServiceTierAuto leaves the choice to the upstream's project or account settings: OpenAI's
	// auto, Messages' auto.
	ServiceTierAuto = "auto"
	// ServiceTierDefault is standard processing: OpenAI's default, Messages' standard_only,
	// Gemini's standard.
	ServiceTierDefault = "default"
	// ServiceTierFlex is the cheaper, slower tier of OpenAI and Gemini.
	ServiceTierFlex = "flex"
	// ServiceTierPriority is the faster tier that costs more: OpenAI's priority and fast,
	// Messages' speed fast, Gemini's priority.
	ServiceTierPriority = "priority"
	// ServiceTierScale and ServiceTierUltrafast are OpenAI tiers no other format has.
	ServiceTierScale     = "scale"
	ServiceTierUltrafast = "ultrafast"
)

// CanonicalServiceTier returns tier with its spelling made the one that decides equality: fast
// is priority (OpenAI takes both for one tier). Every other value, known or not, is returned as
// it is.
func CanonicalServiceTier(tier string) string {
	if tier == "fast" {
		return ServiceTierPriority
	}
	return tier
}

// SourceFormat returns the format r's body came from (the format of Native, else of Origin), or ""
// when it has none.
func (r Request) SourceFormat() string {
	switch {
	case r.Native != nil && r.Native.Format != "":
		return r.Native.Format
	case r.Origin != nil:
		return r.Origin.Format
	}
	return ""
}

// ServiceTierPath returns the field of r's source body that Options.ServiceTier came from, for a
// loss: service_tier, except in a Messages request, where a priority tier is speed, and a
// Gemini one, where it is serviceTier.
func ServiceTierPath(r Request) string {
	switch r.SourceFormat() {
	case FormatAnthropicMessages:
		if CanonicalServiceTier(r.Options.ServiceTier) == ServiceTierPriority {
			return "speed"
		}
	case FormatGeminiGenerate:
		return "serviceTier"
	}
	return "service_tier"
}

// VerbosityPath returns the field of r's source body that Options.Verbosity came from, for a
// loss: verbosity, except in a Responses request, where it is text.verbosity.
func VerbosityPath(r Request) string {
	if r.SourceFormat() == FormatOpenAIResponses {
		return "text.verbosity"
	}
	return "verbosity"
}

// ServiceTierUnsent is the detail of the loss for a service tier an encoder leaves out because
// nothing says its upstream takes the field.
const ServiceTierUnsent = "nothing says this upstream takes a service tier, so it is not sent"

// VerbosityUnsent is ServiceTierUnsent for verbosity.
const VerbosityUnsent = "nothing says this upstream takes a verbosity, so it is not sent"
