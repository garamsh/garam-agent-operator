package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// unimplementedAgentTypes are the types the API admits and this controller
// refuses to build.
var unimplementedAgentTypes = []string{"claude-code", "codex"}

var _ = Describe("Agent type descriptors", func() {
	It("refuses a descriptor with only some fields filled, and accepts sherlock's", func() {
		// The control: every field set, which is what the refusals below differ
		// from in one field each.
		Expect(agentTypeSherlock.implemented()).To(BeTrue())

		onlyWhatADR0025Named := agentTypeDescriptor{configFile: agentTypeSherlock.configFile}
		Expect(onlyWhatADR0025Named.implemented()).To(BeFalse())

		noRenderer := agentTypeSherlock
		noRenderer.renderConfig = nil
		Expect(noRenderer.implemented()).To(BeFalse())

		noExecUser := agentTypeSherlock
		noExecUser.execUserVariable = ""
		Expect(noExecUser.implemented()).To(BeFalse())
	})

	It("resolves the default type named and unset, and refuses claude-code and codex", func() {
		for _, specType := range []string{"", agentTypeDefault} {
			descriptor, ok := resolveAgentType(specType)
			Expect(ok).To(BeTrue(), "type %q", specType)
			Expect(descriptor.stateMountPath).To(Equal(agentTypeSherlock.stateMountPath), "type %q", specType)
		}

		for _, specType := range unimplementedAgentTypes {
			_, ok := resolveAgentType(specType)
			Expect(ok).To(BeFalse(), "type %q", specType)
		}
	})
})
