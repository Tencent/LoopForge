package domain

const (
	AssistantProposalAccepted = "accepted"
	AssistantProposalDeclined = "declined"
	// AssistantProposalMuted declines and turns proposals off for the rest
	// of the conversation.
	AssistantProposalMuted = "muted"
	// AssistantProposalExpired means nobody answered a blocking proposal in
	// time and Main Bot went on to do the work itself.
	AssistantProposalExpired = "expired"
	// AssistantDraftRejected means the independent AI reviewer or fast-path
	// policy rejected a write in "帮我批准" mode without asking a person.
	AssistantDraftRejected = "rejected"
)
