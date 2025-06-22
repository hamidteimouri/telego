package telego

import objs "github.com/hamidteimouri/telego/objects"

// GeneralForumTopicManager is a special object for managing genreal forum topics
type GeneralForumTopicManager struct {
	bot          *Bot
	chatId       int
	chatIdString string
}

/*Edit edit the name of the 'General' topic in a forum supergroup chat. The bot must be an administrator in the chat for this to work and must have can_manage_topics administrator rights. Returns True on success.*/
func (f *GeneralForumTopicManager) Edit(name string) (*objs.Result[bool], error) {
	return f.bot.apiInterface.EditGeneralForumTopic(f.chatId, f.chatIdString, name)
}

/*Close closes an open 'General' topic in a forum supergroup chat. The bot must be an administrator in the chat for this to work and must have the can_manage_topics administrator rights. Returns True on success.*/
func (f *GeneralForumTopicManager) Close() (*objs.Result[bool], error) {
	return f.bot.apiInterface.CloseGeneralForumTopic(f.chatId, f.chatIdString)
}

/*Reopen reopens a closed 'General' topic in a forum supergroup chat. The bot must be an administrator in the chat for this to work and must have the can_manage_topics administrator rights. The topic will be automatically unhidden if it was hidden. Returns True on success.*/
func (f *GeneralForumTopicManager) Reopen() (*objs.Result[bool], error) {
	return f.bot.apiInterface.ReopenGeneralForumTopic(f.chatId, f.chatIdString)
}

/*Hide  hides the 'General' topic in a forum supergroup chat. The bot must be an administrator in the chat for this to work and must have the can_manage_topics administrator rights. The topic will be automatically closed if it was open. Returns True on success.*/
func (f *GeneralForumTopicManager) Hide() (*objs.Result[bool], error) {
	return f.bot.apiInterface.HideGeneralForumTopic(f.chatId, f.chatIdString)
}

/*Unhide unhides the 'General' topic in a forum supergroup chat. The bot must be an administrator in the chat for this to work and must have the can_manage_topics administrator rights. Returns True on success.*/
func (f *GeneralForumTopicManager) Unhide() (*objs.Result[bool], error) {
	return f.bot.apiInterface.UnhideGeneralForumTopic(f.chatId, f.chatIdString)
}

/*UnpinAllMesages clears the list of pinned messages in a General forum topic. The bot must be an administrator in the chat for this to work and must have the can_pin_messages administrator right in the supergroup. Returns True on success.*/
func (f *GeneralForumTopicManager) UnpinAllMesages() (*objs.Result[bool], error) {
	return f.bot.apiInterface.UnpinAllGeneralForumTopicMessages(f.chatId, f.chatIdString)
}
