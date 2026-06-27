package wizard

type wizardStep struct {
	step  step
	label string
}

var wizardSteps = []wizardStep{
	{stepProviders, "Providers"},
	{stepDirectories, "Data Directory"},
	{stepNetworking, "Gateway"},
	{stepAgent, "Agent & Context"},
	{stepTelegram, "Telegram"},
	{stepReview, "Review & Save"},
}