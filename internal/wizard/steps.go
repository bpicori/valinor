package wizard

type wizardStep struct {
	step  step
	label string
}

var wizardSteps = []wizardStep{
	{stepProviders, "Providers"},
	{stepDirectories, "Directories"},
	{stepNetworking, "Gateway & Web"},
	{stepAgent, "Agent & Context"},
	{stepTelegram, "Telegram"},
	{stepScheduler, "Scheduler"},
	{stepSandbox, "Sandbox"},
	{stepReview, "Review & Save"},
}