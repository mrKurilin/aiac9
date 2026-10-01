package terminal

func init() {
	setCommands([]Command{
		{Value: "/help", Description: "помощь", Submit: true},
		{Value: "/rag", Description: "режим"},
		{Value: "/rag on", Description: "включить", Submit: true},
		{Value: "/rag off", Description: "выключить", Submit: true},
	})
}
