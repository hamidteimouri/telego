package tba

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	cfgs "github.com/hamidteimouri/telego/configs"
	errs "github.com/hamidteimouri/telego/errors"
	logger "github.com/hamidteimouri/telego/logger"
	objs "github.com/hamidteimouri/telego/objects"
	"github.com/hamidteimouri/telego/parser"
)

var interfaceCreated = false

// BotAPIInterface is the interface which connects the telegram bot API to the bot.
type BotAPIInterface struct {
	botConfigs           *cfgs.BotConfigs
	updateRoutineRunning bool
	updateChannel        *chan *objs.Update
	chatUpadateChannel   *chan *objs.ChatUpdate
	updateRoutineChannel chan bool
	updateParser         *parser.UpdateParser
	lastOffset           int
	logger               *logger.BotLogger
}

/*StartUpdateRoutine starts the update routine to receive updates from api sever*/
func (bai *BotAPIInterface) StartUpdateRoutine() error {
	if !bai.botConfigs.Webhook {
		if bai.updateRoutineRunning {
			return &errs.UpdateRoutineAlreadyStarted{}
		}
		bai.updateRoutineRunning = true
		bai.updateRoutineChannel = make(chan bool)
		go bai.startReceiving()
		return nil
	} else {
		return errors.New("webhook option is true")
	}
}

/*StopUpdateRoutine stops the update routine*/
func (bai *BotAPIInterface) StopUpdateRoutine() {
	if bai.updateRoutineRunning {
		bai.updateRoutineRunning = false
		bai.updateRoutineChannel <- true
	}
}

/*GetUpdateChannel returns the update channel*/
func (bai *BotAPIInterface) GetUpdateChannel() *chan *objs.Update {
	return bai.updateChannel
}

/*GetChatUpdateChannel returnes the chat update channel*/
func (bai *BotAPIInterface) GetChatUpdateChannel() *chan *objs.ChatUpdate {
	return bai.chatUpadateChannel
}

// GetUpdateParser returns the bot's update parser that has been initialized upon bot creation.
func (bai *BotAPIInterface) GetUpdateParser() *parser.UpdateParser {
	return bai.updateParser
}

func (bai *BotAPIInterface) startReceiving() {
	cl := httpSenderClient{botApi: bai.botConfigs.BotAPI, apiKey: bai.botConfigs.APIKey}
loop:
	for {
		time.Sleep(bai.botConfigs.UpdateConfigs.UpdateFrequency)
		select {
		case <-bai.updateRoutineChannel:
			break loop
		default:
			args := objs.GetUpdatesArgs{Offset: bai.lastOffset + 1, Limit: bai.botConfigs.UpdateConfigs.Limit, Timeout: bai.botConfigs.UpdateConfigs.Timeout}
			if bai.botConfigs.UpdateConfigs.AllowedUpdates != nil {
				args.AllowedUpdates = bai.botConfigs.UpdateConfigs.AllowedUpdates
			}
			res, err := cl.sendHttpReqJson("getUpdates", &args)
			if err != nil {
				bai.logger.GetRaw().Println("Error receiving updates.", err)
				continue loop
			}
			err = bai.parseUpdateresults(res)
			if err != nil {
				bai.logger.GetRaw().Println("Error parsing the result of the update. " + err.Error())
			}
		}
	}
}

func (bai *BotAPIInterface) parseUpdateresults(body []byte) error {
	of, err := bai.ParseUpdate(
		body,
	)
	if err != nil {
		return err
	}
	if of > bai.lastOffset {
		bai.lastOffset = of
	}
	return nil
}

// ParseUpdate parses the received update and returns the last update offset.
func (bai *BotAPIInterface) ParseUpdate(body []byte) (int, error) {
	def := &objs.Result[json.RawMessage]{}
	err2 := json.Unmarshal(body, def)
	if err2 != nil {
		return 0, err2
	}
	if !def.Ok {
		return 0, &errs.MethodNotSentError{Method: "getUpdates", Reason: "server returned false for \"ok\" field."}
	}
	ur := &objs.Result[[]*objs.Update]{}
	err := json.Unmarshal(body, ur)
	if err != nil {
		return 0, err
	}

	lastOffset := 0
	for _, val := range ur.Result {
		if val.Update_id > lastOffset {
			lastOffset = val.Update_id
		}
		go bai.updateParser.ExecuteChain(val)
	}
	return lastOffset, nil
}

func (bai *BotAPIInterface) isChatIdOk(chatIdInt int, chatIdString string) bool {
	if chatIdInt == 0 {
		return chatIdString != ""
	} else {
		return chatIdString == ""
	}
}

/*GetMe gets the bot info*/
func (bai *BotAPIInterface) GetMe() (*objs.Result[*objs.User], error) {
	res, err := bai.SendCustom("getMe", nil, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[*objs.User]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*
SendMessage sends a message to the user. chatIdInt is used for all chats but channles and chatidString is used for channels (in form of @channleusername) and only of them has be populated, otherwise ChatIdProblem error will be returned.
"chatId" and "text" arguments are required. other arguments are optional for bot api.
*/
func (bai *BotAPIInterface) SendMessage(chatIdInt int, chatIdString, text, parseMode string, entities []objs.MessageEntity, disable_web_page_preview, disable_notification, allow_sending_without_reply, ProtectContent bool, reply_to_message_id, messageThreadId int, reply_markup objs.ReplyMarkup) (*objs.Result[*objs.Message], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		def := bai.fixTheDefaultArguments(chatIdInt, reply_to_message_id, messageThreadId, chatIdString, disable_notification, allow_sending_without_reply, ProtectContent, reply_markup)
		args := &objs.SendMessageArgs{
			Text:                        text,
			DisableWebPagePreview:       disable_web_page_preview,
			DefaultSendMethodsArguments: def,
			ParseMode:                   parseMode,
			Entities:                    entities,
		}

		res, err := bai.SendCustom("sendMessage", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[*objs.Message]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "sendMessage"}
	}
}

/*
ForwardMessage forwards a message from a user or channel to a user or channel. If the source or destination (or both) of the forwarded message is a channel, only string chat ids should be given to the function, and if it is user only int chat ids should be given.
"chatId", "fromChatId" and "messageId" arguments are required. other arguments are optional for bot api.
*/
func (bai *BotAPIInterface) ForwardMessage(chatIdInt, fromChatIdInt int, chatIdString, fromChatIdString string, disableNotif, ProtectContent bool, messageId, messageThreadId int) (*objs.Result[*objs.Message], error) {
	if (chatIdInt != 0 && chatIdString != "") && (fromChatIdInt != 0 && fromChatIdString != "") {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) && bai.isChatIdOk(fromChatIdInt, fromChatIdString) {
		fm := &objs.ForwardMessageArgs{
			DisableNotification: disableNotif,
			MessageId:           messageId,
			ProtectContent:      ProtectContent,
			MessageThreadId:     messageThreadId,
		}
		fm.ChatId = bai.fixChatId(chatIdInt, chatIdString)
		fm.FromChatId = bai.fixChatId(fromChatIdInt, fromChatIdString)
		res, err := bai.SendCustom("forwardMessage", fm, false, nil, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[*objs.Message]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString or fromChatIdInt or fromChatIdString", MethodName: "forwardMessage"}
	}
}

/*
SendPhoto sends a photo (file,url,telegramId) to a channel (chatIdString) or a chat (chatIdInt)
"chatId" and "photo" arguments are required. other arguments are optional for bot api.
*/
func (bai *BotAPIInterface) SendPhoto(chatIdInt int, chatIdString, photo string, photoFile *os.File, caption, parseMode string, reply_to_message_id, messageThreadId int, disable_notification, allow_sending_without_reply, protectContent, hasSpoiler bool, reply_markup objs.ReplyMarkup, captionEntities []objs.MessageEntity) (*objs.Result[*objs.Message], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.SendPhotoArgs{
			DefaultSendMethodsArguments: bai.fixTheDefaultArguments(
				chatIdInt, reply_to_message_id, messageThreadId, chatIdString, disable_notification,
				allow_sending_without_reply, protectContent, reply_markup,
			),
			Photo:           photo,
			Caption:         caption,
			ParseMode:       parseMode,
			CaptionEntities: captionEntities,
			HasSpoiler:      hasSpoiler,
		}
		var res []byte
		var err error
		if photoFile != nil {
			res, err = bai.SendCustom("sendPhoto", args, true, photoFile, nil)
		} else {
			res, err = bai.SendCustom("sendPhoto", args, false, nil, nil)
		}
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[*objs.Message]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "sendPhoto"}
	}
}

/*
SendVideo sends a video (file,url,telegramId) to a channel (chatIdString) or a chat (chatIdInt)
"chatId" and "video" arguments are required. other arguments are optional for bot api. (to ignore int arguments, pass 0)
*/
func (bai *BotAPIInterface) SendVideo(chatIdInt int, chatIdString, video string, videoFile *os.File, caption, parseMode string, reply_to_message_id, messageThreadId int, thumb string, thumbFile *os.File, disable_notification, allow_sending_without_reply, protectContent, hasSpoiler bool, captionEntities []objs.MessageEntity, duration int, supportsStreaming bool, reply_markup objs.ReplyMarkup) (*objs.Result[*objs.Message], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.SendVideoArgs{
			DefaultSendMethodsArguments: bai.fixTheDefaultArguments(
				chatIdInt, reply_to_message_id, messageThreadId, chatIdString, disable_notification,
				allow_sending_without_reply, protectContent, reply_markup,
			),
			Video:             video,
			Caption:           caption,
			Thumb:             thumb,
			ParseMode:         parseMode,
			CaptionEntities:   captionEntities,
			Duration:          duration,
			SupportsStreaming: supportsStreaming,
			HasSpoiler:        hasSpoiler,
		}
		res, err := bai.SendCustom("sendVideo", args, true, videoFile, thumbFile)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[*objs.Message]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "sendVideo"}
	}
}

/*
SendAudio sends an audio (file,url,telegramId) to a channel (chatIdString) or a chat (chatIdInt)
"chatId" and "audio" arguments are required. other arguments are optional for bot api. (to ignore int arguments, pass 0,to ignore string arguments pass "")
*/
func (bai *BotAPIInterface) SendAudio(chatIdInt int, chatIdString, audio string, audioFile *os.File, caption, parseMode string, reply_to_message_id, messageThreadId int, thumb string, thumbFile *os.File, disable_notification, allow_sending_without_reply, ProtectContent bool, captionEntities []objs.MessageEntity, duration int, performer, title string, reply_markup objs.ReplyMarkup) (*objs.Result[*objs.Message], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.SendAudioArgs{
			DefaultSendMethodsArguments: bai.fixTheDefaultArguments(
				chatIdInt, reply_to_message_id, messageThreadId, chatIdString, disable_notification,
				allow_sending_without_reply, ProtectContent, reply_markup,
			),
			Audio:           audio,
			Caption:         caption,
			Thumb:           thumb,
			ParseMode:       parseMode,
			CaptionEntities: captionEntities,
			Duration:        duration,
			Performer:       performer,
			Title:           title,
		}
		res, err := bai.SendCustom("sendAudio", args, true, audioFile, thumbFile)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[*objs.Message]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "sendAudio"}
	}
}

/*
sSendDocument sends a document (file,url,telegramId) to a channel (chatIdString) or a chat (chatIdInt)
"chatId" and "document" arguments are required. other arguments are optional for bot api. (to ignore int arguments, pass 0)
*/
func (bai *BotAPIInterface) SendDocument(chatIdInt int, chatIdString, document string, documentFile *os.File, caption, parseMode string, reply_to_message_id, messageThreadId int, thumb string, thumbFile *os.File, disable_notification, allow_sending_without_reply, ProtectContent bool, captionEntities []objs.MessageEntity, DisableContentTypeDetection bool, reply_markup objs.ReplyMarkup) (*objs.Result[*objs.Message], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.SendDocumentArgs{
			DefaultSendMethodsArguments: bai.fixTheDefaultArguments(
				chatIdInt, reply_to_message_id, messageThreadId, chatIdString, disable_notification,
				allow_sending_without_reply, ProtectContent, reply_markup,
			),
			Document:                    document,
			Caption:                     caption,
			Thumb:                       thumb,
			ParseMode:                   parseMode,
			CaptionEntities:             captionEntities,
			DisableContentTypeDetection: DisableContentTypeDetection,
		}
		res, err := bai.SendCustom("sendDocument", args, true, documentFile, thumbFile)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[*objs.Message]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "sendDocument"}
	}
}

/*
SendAnimation sends an animation (file,url,telegramId) to a channel (chatIdString) or a chat (chatIdInt)
"chatId" and "animation" arguments are required. other arguments are optional for bot api. (to ignore int arguments, pass 0)
*/
func (bai *BotAPIInterface) SendAnimation(chatIdInt int, chatIdString, animation string, animationFile *os.File, caption, parseMode string, width, height, duration int, reply_to_message_id, messageThreadId int, thumb string, thumbFile *os.File, disable_notification, allow_sending_without_reply, protectContent, hasSpoiler bool, captionEntities []objs.MessageEntity, reply_markup objs.ReplyMarkup) (*objs.Result[*objs.Message], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.SendAnimationArgs{
			DefaultSendMethodsArguments: bai.fixTheDefaultArguments(
				chatIdInt, reply_to_message_id, messageThreadId, chatIdString, disable_notification,
				allow_sending_without_reply, protectContent, reply_markup,
			),
			Animation:       animation,
			Caption:         caption,
			Thumb:           thumb,
			ParseMode:       parseMode,
			CaptionEntities: captionEntities,
			Width:           width,
			Height:          height,
			Duration:        duration,
			HasSpoiler:      hasSpoiler,
		}
		res, err := bai.SendCustom("sendAnimation", args, true, animationFile, thumbFile)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[*objs.Message]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "sendAnimation"}
	}
}

/*
sSendVoice sends a voice (file,url,telegramId) to a channel (chatIdString) or a chat (chatIdInt)
"chatId" and "voice" arguments are required. other arguments are optional for bot api. (to ignore int arguments, pass 0)
*/
func (bai *BotAPIInterface) SendVoice(chatIdInt int, chatIdString, voice string, voiceFile *os.File, caption, parseMode string, duration int, reply_to_message_id, messageThreadId int, disable_notification, allow_sending_without_reply, ProtectContent bool, captionEntities []objs.MessageEntity, reply_markup objs.ReplyMarkup) (*objs.Result[*objs.Message], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.SendVoiceArgs{
			DefaultSendMethodsArguments: bai.fixTheDefaultArguments(
				chatIdInt, reply_to_message_id, messageThreadId, chatIdString, disable_notification,
				allow_sending_without_reply, ProtectContent, reply_markup,
			),
			Voice:           voice,
			Caption:         caption,
			ParseMode:       parseMode,
			CaptionEntities: captionEntities,
			Duration:        duration,
		}
		res, err := bai.SendCustom("sendVoice", args, true, voiceFile)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[*objs.Message]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "sendVoice"}
	}
}

/*
SendVideoNote sends a video note (file,url,telegramId) to a channel (chatIdString) or a chat (chatIdInt)
"chatId" and "videoNote" arguments are required. other arguments are optional for bot api. (to ignore int arguments, pass 0)
Note that sending video note by URL is not supported by telegram.
*/
func (bai *BotAPIInterface) SendVideoNote(chatIdInt int, chatIdString, videoNote string, videoNoteFile *os.File, caption, parseMode string, length, duration int, reply_to_message_id, messageThreadId int, thumb string, thumbFile *os.File, disable_notification, allow_sending_without_reply, ProtectContent bool, captionEntities []objs.MessageEntity, reply_markup objs.ReplyMarkup) (*objs.Result[*objs.Message], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.SendVideoNoteArgs{
			DefaultSendMethodsArguments: bai.fixTheDefaultArguments(
				chatIdInt, reply_to_message_id, messageThreadId, chatIdString, disable_notification,
				allow_sending_without_reply, ProtectContent, reply_markup,
			),
			VideoNote:       videoNote,
			Caption:         caption,
			Thumb:           thumb,
			ParseMode:       parseMode,
			CaptionEntities: captionEntities,
			Length:          length,
			Duration:        duration,
		}
		res, err := bai.SendCustom("sendVideoNote", args, true, videoNoteFile, thumbFile)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[*objs.Message]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "sendVideoNote"}
	}
}

/*
SendMediaGroup sends an album of media (file,url,telegramId) to a channel (chatIdString) or a chat (chatIdInt)
"chatId" and "media" arguments are required. other arguments are optional for bot api. (to ignore int arguments, pass 0)
*/
func (bai *BotAPIInterface) SendMediaGroup(chatIdInt int, chatIdString string, reply_to_message_id, messageThreadId int, media []objs.InputMedia, disable_notification, allow_sending_without_reply, ProtectContent bool, reply_markup objs.ReplyMarkup, files ...*os.File) (*objs.Result[[]objs.Message], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.SendMediaGroupArgs{
			DefaultSendMethodsArguments: bai.fixTheDefaultArguments(
				chatIdInt, reply_to_message_id, messageThreadId, chatIdString, disable_notification,
				allow_sending_without_reply, ProtectContent, reply_markup,
			),
			Media: media,
		}
		res, err := bai.SendCustom("sendMediaGroup", args, true, files...)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[[]objs.Message]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "sendMediaGRoup"}
	}
}

/*
SendLocation sends a location to a channel (chatIdString) or a chat (chatIdInt)
"chatId","latitude" and "longitude" arguments are required. other arguments are optional for bot api. (to ignore int arguments, pass 0)
*/
func (bai *BotAPIInterface) SendLocation(chatIdInt int, chatIdString string, latitude, longitude, horizontalAccuracy float32, livePeriod, heading, proximityAlertRadius, reply_to_message_id, messageThreadId int, disable_notification, allow_sending_without_reply, ProtectContent bool, reply_markup objs.ReplyMarkup) (*objs.Result[*objs.Message], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.SendLocationArgs{
			DefaultSendMethodsArguments: bai.fixTheDefaultArguments(
				chatIdInt, reply_to_message_id, messageThreadId, chatIdString, disable_notification,
				allow_sending_without_reply, ProtectContent, reply_markup,
			),
			Latitude:             latitude,
			Longitude:            longitude,
			HorizontalAccuracy:   horizontalAccuracy,
			LivePeriod:           livePeriod,
			Heading:              heading,
			ProximityAlertRadius: proximityAlertRadius,
		}
		res, err := bai.SendCustom("sendLocation", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[*objs.Message]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "sendLocation"}
	}
}

/*
EditMessageLiveLocation edits a live location sent to a channel (chatIdString) or a chat (chatIdInt)
"chatId","latitude" and "longitude" arguments are required. other arguments are optional for bot api. (to ignore int arguments, pass 0)
*/
func (bai *BotAPIInterface) EditMessageLiveLocation(chatIdInt int, chatIdString, inlineMessageId string, messageId int, latitude, longitude, horizontalAccuracy float32, heading, proximityAlertRadius int, reply_markup *objs.InlineKeyboardMarkup) (*objs.Result[json.RawMessage], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.EditMessageLiveLocationArgs{
			InlineMessageId:      inlineMessageId,
			MessageId:            messageId,
			Latitude:             latitude,
			Longitude:            longitude,
			HorizontalAccuracy:   horizontalAccuracy,
			Heading:              heading,
			ProximityAlertRadius: proximityAlertRadius,
			ReplyMarkup:          reply_markup,
		}
		args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
		res, err := bai.SendCustom("editMessageLiveLocation", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[json.RawMessage]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "editMessageLiveLocation"}
	}
}

/*
StopMessageLiveLocation stops a live location sent to a channel (chatIdString) or a chat (chatIdInt)
"chatId" argument is required. other arguments are optional for bot api. (to ignore int arguments, pass 0)
*/
func (bai *BotAPIInterface) StopMessageLiveLocation(chatIdInt int, chatIdString, inlineMessageId string, messageId int, replyMarkup *objs.InlineKeyboardMarkup) (*objs.Result[json.RawMessage], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.StopMessageLiveLocationArgs{
			InlineMessageId: inlineMessageId,
			MessageId:       messageId,
			ReplyMarkup:     replyMarkup,
		}
		args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
		res, err := bai.SendCustom("stopMessageLiveLocation", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[json.RawMessage]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "stopMessageLiveLocation"}
	}
}

/*
SendVenue sends a venue to a channel (chatIdString) or a chat (chatIdInt)
"chatId","latitude","longitude","title" and "address" arguments are required. other arguments are optional for bot api. (to ignore int arguments, pass 0)
*/
func (bai *BotAPIInterface) SendVenue(chatIdInt int, chatIdString string, latitude, longitude float32, title, address, fourSquareId, fourSquareType, googlePlaceId, googlePlaceType string, reply_to_message_id, messageThreadId int, disable_notification, allow_sending_without_reply, ProtectContent bool, reply_markup objs.ReplyMarkup) (*objs.Result[*objs.Message], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.SendVenueArgs{
			DefaultSendMethodsArguments: bai.fixTheDefaultArguments(
				chatIdInt, reply_to_message_id, messageThreadId, chatIdString, disable_notification,
				allow_sending_without_reply, ProtectContent, reply_markup,
			),
			Latitude:        latitude,
			Longitude:       longitude,
			Title:           title,
			Address:         address,
			FoursquareId:    fourSquareId,
			FoursquareType:  fourSquareType,
			GooglePlaceId:   googlePlaceId,
			GooglePlaceType: googlePlaceType,
		}
		res, err := bai.SendCustom("sendVnue", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[*objs.Message]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "sendcontact"}
	}
}

/*
SendContact sends a contact to a channel (chatIdString) or a chat (chatIdInt)
"chatId","phoneNumber" and "firstName" arguments are required. other arguments are optional for bot api. (to ignore int arguments, pass 0)
*/
func (bai *BotAPIInterface) SendContact(chatIdInt int, chatIdString, phoneNumber, firstName, lastName, vCard string, reply_to_message_id, messageThreadId int, disable_notification, allow_sending_without_reply, ProtectContent bool, reply_markup objs.ReplyMarkup) (*objs.Result[*objs.Message], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.SendContactArgs{
			DefaultSendMethodsArguments: bai.fixTheDefaultArguments(
				chatIdInt, reply_to_message_id, messageThreadId, chatIdString, disable_notification,
				allow_sending_without_reply, ProtectContent, reply_markup,
			),
			PhoneNumber: phoneNumber,
			FirstName:   firstName,
			LastName:    lastName,
			Vcard:       vCard,
		}
		res, err := bai.SendCustom("sendContact", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[*objs.Message]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "sendContact"}
	}
}

/*
SendPoll sends a poll to a channel (chatIdString) or a chat (chatIdInt)
"chatId","phoneNumber" and "firstName" arguments are required. other arguments are optional for bot api. (to ignore int arguments, pass 0)
*/
func (bai *BotAPIInterface) SendPoll(chatIdInt int, chatIdString, question string, options []string, isClosed, isAnonymous bool, pollType string, allowMultipleAnswers bool, correctOptionIndex int, explanation, explanationParseMode string, explanationEntities []objs.MessageEntity, openPeriod, closeDate int, reply_to_message_id, messageThreadId int, disable_notification, allow_sending_without_reply, ProtectContent bool, reply_markup objs.ReplyMarkup) (*objs.Result[*objs.Message], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.SendPollArgs{
			DefaultSendMethodsArguments: bai.fixTheDefaultArguments(
				chatIdInt, reply_to_message_id, messageThreadId, chatIdString, disable_notification,
				allow_sending_without_reply, ProtectContent, reply_markup,
			),
			Question:              question,
			Options:               options,
			IsClosed:              isClosed,
			IsAnonymous:           isAnonymous,
			Type:                  pollType,
			AllowsMultipleAnswers: allowMultipleAnswers,
			CorrectOptionId:       correctOptionIndex,
			Explanation:           explanation,
			ExplanationParseMode:  explanationParseMode,
			ExplanationEntities:   explanationEntities,
			OpenPeriod:            openPeriod,
			CloseDate:             closeDate,
		}
		res, err := bai.SendCustom("sendPoll", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[*objs.Message]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "sendPoll"}
	}
}

/*
SendDice sends a dice message to a channel (chatIdString) or a chat (chatIdInt)
"chatId" argument is required. other arguments are optional for bot api. (to ignore int arguments, pass 0)
*/
func (bai *BotAPIInterface) SendDice(chatIdInt int, chatIdString, emoji string, reply_to_message_id, messageThreadId int, disable_notification, allow_sending_without_reply, ProtectContent bool, reply_markup objs.ReplyMarkup) (*objs.Result[*objs.Message], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.SendDiceArgs{
			DefaultSendMethodsArguments: bai.fixTheDefaultArguments(
				chatIdInt, reply_to_message_id, messageThreadId, chatIdString, disable_notification,
				allow_sending_without_reply, ProtectContent, reply_markup,
			),
			Emoji: emoji,
		}
		res, err := bai.SendCustom("sendDice", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[*objs.Message]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "sendDice"}
	}
}

/*
SendChatAction sends a chat action message to a channel (chatIdString) or a chat (chatIdInt)
"chatId" argument is required. other arguments are optional for bot api. (to ignore int arguments, pass 0)
*/
func (bai *BotAPIInterface) SendChatAction(chatIdInt, messageThreadId int, chatIdString, chatAction string) (*objs.Result[*objs.Message], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.SendChatActionArgs{
			Action:           chatAction,
			MessageThreaddId: messageThreadId,
		}
		args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
		res, err := bai.SendCustom("sendChatAction", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[*objs.Message]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "sendChatAction"}
	}
}

/*GetUserProfilePhotos gets the user profile photos*/
func (bai *BotAPIInterface) GetUserProfilePhotos(userId, offset, limit int) (*objs.Result[*objs.UserProfilePhotos], error) {
	args := &objs.GetUserProfilePhototsArgs{UserId: userId, Offset: offset, Limit: limit}
	res, err := bai.SendCustom("getUserProfilePhotos", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[*objs.UserProfilePhotos]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*GetFile gets the file based on the given file id and returns the file object. */
func (bai *BotAPIInterface) GetFile(fileId string) (*objs.Result[*objs.File], error) {
	args := &objs.GetFileArgs{FileId: fileId}
	res, err := bai.SendCustom("getFile", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[*objs.File]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*
DownloadFile downloads a file from telegram servers and saves it into the given file.

This method closes the given file. If the file is nil, this method will create a file based on the name of the file stored in telegram servers.
*/
func (bai *BotAPIInterface) DownloadFile(fileObject *objs.File, file *os.File) error {
	url := "https://api.telegram.org/file/bot" + bai.botConfigs.APIKey + "/" + fileObject.FilePath
	res, err := http.Get(url)
	if err != nil {
		return err
	}
	if file == nil {
		ar := strings.Split(fileObject.FilePath, "/")
		name := ar[len(ar)-1]
		var er error
		file, er = os.OpenFile(name, os.O_CREATE|os.O_WRONLY, 0666)
		if er != nil {
			return er
		}
	}
	if res.StatusCode < 300 {
		_, err2 := io.Copy(file, res.Body)
		if err2 != nil {
			return err2
		}
		err3 := file.Close()
		return err3
	} else {
		return &errs.MethodNotSentError{Method: "getFile", Reason: "server returned status code " + strconv.Itoa(res.StatusCode)}
	}
}

/*BanChatMember bans a chat member*/
func (bai *BotAPIInterface) BanChatMember(chatIdInt int, chatIdString string, userId, untilDate int, revokeMessages bool) (*objs.Result[bool], error) {
	args := &objs.BanChatMemberArgs{
		UserId:         userId,
		UntilDate:      untilDate,
		RevokeMessages: revokeMessages,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("banChatMember", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*UnbanChatMember unbans a chat member*/
func (bai *BotAPIInterface) UnbanChatMember(chatIdInt int, chatIdString string, userId int, onlyIfBanned bool) (*objs.Result[bool], error) {
	args := &objs.UnbanChatMemberArgsArgs{
		UserId:       userId,
		OnlyIfBanned: onlyIfBanned,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("unbanChatMember", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*RestrictChatMember restricts a chat member*/
func (bai *BotAPIInterface) RestrictChatMember(chatIdInt int, chatIdString string, userId int, permissions objs.ChatPermissions, useIndependentChatPermissions bool, untilDate int) (*objs.Result[bool], error) {
	args := &objs.RestrictChatMemberArgs{
		UserId:                        userId,
		Permission:                    permissions,
		UseIndependentChatPermissions: useIndependentChatPermissions,
		UntilDate:                     untilDate,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("restrictChatMember", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*PromoteChatMember promotes a chat member*/
func (bai *BotAPIInterface) PromoteChatMember(chatIdInt int, chatIdString string, userId int, isAnonymous, canManageChat, canPostmessages, canEditMessages, canDeleteMessages, canPostStories, canEditStories, canDeleteStoreis, canManageVideoChats, canRestrictMembers, canPromoteMembers, canChangeInfo, canInviteUsers, canPinMessages, canManageTopics bool) (*objs.Result[bool], error) {
	args := &objs.PromoteChatMemberArgs{
		UserId:              userId,
		IsAnonymous:         isAnonymous,
		CanManageChat:       canManageChat,
		CanPostMessages:     canPostmessages,
		CanEditMessages:     canEditMessages,
		CanDeleteMessages:   canDeleteMessages,
		CanPostStories:      canPostmessages,
		CanEditStories:      canEditMessages,
		CanDeleteStories:    canDeleteMessages,
		CanManageVideoChats: canManageVideoChats,
		CanRestrictMembers:  canRestrictMembers,
		CanPromoteMembers:   canPromoteMembers,
		CanChangeInfo:       canChangeInfo,
		CanInviteUsers:      canInviteUsers,
		CanPinMessages:      canPinMessages,
		CanManageTopics:     canManageTopics,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("promoteChatMember", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*SetMyDefaultAdministratorRights sets the admin rights*/
func (bai *BotAPIInterface) SetMyDefaultAdministratorRights(forChannels, isAnonymous, canManageChat, canPostmessages, canEditMessages, canDeleteMessages, canManageVideoChats, canRestrictMembers, canPromoteMembers, canChangeInfo, canInviteUsers, canPinMessages bool) (*objs.Result[bool], error) {
	args := &objs.MyDefaultAdministratorRightsArgs{
		Rights: &objs.ChatAdministratorRights{
			IsAnonymous:         isAnonymous,
			CanManageChat:       canManageChat,
			CanPostMessages:     canPostmessages,
			CanEditMessages:     canEditMessages,
			CanDeleteMessages:   canDeleteMessages,
			CanManageVideoChats: canManageVideoChats,
			CanRestrictMembers:  canRestrictMembers,
			CanPromoteMembers:   canPromoteMembers,
			CanChangeInfo:       canChangeInfo,
			CanInviteUsers:      canInviteUsers,
			CanPinMessages:      canPinMessages,
		},
		ForChannels: forChannels,
	}
	res, err := bai.SendCustom("setMyDefaultAdministratorRights", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*GetMyDefaultAdministratorRights gets the admin rights*/
func (bai *BotAPIInterface) GetMyDefaultAdministratorRights(forChannels bool) (*objs.Result[*objs.ChatAdministratorRights], error) {
	args := &objs.MyDefaultAdministratorRightsArgs{
		ForChannels: forChannels,
	}
	res, err := bai.SendCustom("getMyDefaultAdministratorRights", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[*objs.ChatAdministratorRights]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*SetChatAdministratorCustomTitle sets a custom title for the administrator.*/
func (bai *BotAPIInterface) SetChatAdministratorCustomTitle(chatIdInt int, chatIdString string, userId int, customTitle string) (*objs.Result[bool], error) {
	args := &objs.SetChatAdministratorCustomTitleArgs{
		UserId:      userId,
		CustomTitle: customTitle,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("setChatAdministratorCustomTitle", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*BanOrUnbanChatSenderChat bans or unbans a channel in the group..*/
func (bai *BotAPIInterface) BanOrUnbanChatSenderChat(chatIdInt int, chatIdString string, senderChatId int, ban bool) (*objs.Result[bool], error) {
	args := &objs.BanChatSenderChatArgs{
		SenderChatId: senderChatId,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	var method string
	if ban {
		method = "banChatSenderChat"
	} else {
		method = "unbanChatSenderChat"
	}
	res, err := bai.SendCustom(method, args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*SetChatPermissions sets default permissions for all users in the chat.*/
func (bai *BotAPIInterface) SetChatPermissions(chatIdInt int, chatIdString string, useIndependentChatPermissions bool, permissions objs.ChatPermissions) (*objs.Result[bool], error) {
	args := &objs.SetChatPermissionsArgs{
		Permissions:                   permissions,
		UseIndependentChatPermissions: useIndependentChatPermissions,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("setChatPermissions", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*ExportChatInviteLink exports the chat invite link and returns the new invite link as string.*/
func (bai *BotAPIInterface) ExportChatInviteLink(chatIdInt int, chatIdString string) (*objs.Result[string], error) {
	args := &objs.DefaultChatArgs{}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("exprotChatInviteLink", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[string]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*CreateChatInviteLink creates a new invite link for the chat.*/
func (bai *BotAPIInterface) CreateChatInviteLink(chatIdInt int, chatIdString, name string, expireDate, memberLimit int, createsJoinRequest bool) (*objs.Result[*objs.ChatInviteLink], error) {
	args := &objs.CreateChatInviteLinkArgs{
		Name:               name,
		ExpireDate:         expireDate,
		MemberLimit:        memberLimit,
		CreatesjoinRequest: createsJoinRequest,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("createChatInviteLink", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[*objs.ChatInviteLink]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*EditChatInviteLink edits an existing invite link for the chat.*/
func (bai *BotAPIInterface) EditChatInviteLink(chatIdInt int, chatIdString, inviteLink, name string, expireDate, memberLimit int, createsJoinRequest bool) (*objs.Result[*objs.ChatInviteLink], error) {
	args := &objs.EditChatInviteLinkArgs{
		InviteLink:         inviteLink,
		Name:               name,
		ExpireDate:         expireDate,
		MemberLimit:        memberLimit,
		CreatesjoinRequest: createsJoinRequest,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("editChatInviteLink", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[*objs.ChatInviteLink]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*RevokeChatInviteLink revokes the given invite link.*/
func (bai *BotAPIInterface) RevokeChatInviteLink(chatIdInt int, chatIdString, inviteLink string) (*objs.Result[*objs.ChatInviteLink], error) {
	args := &objs.RevokeChatInviteLinkArgs{
		InviteLink: inviteLink,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("revokeChatInviteLink", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[*objs.ChatInviteLink]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*ApproveChatJoinRequest approves a request from the given user to join the chat.*/
func (bai *BotAPIInterface) ApproveChatJoinRequest(chatIdInt int, chatIdString string, userId int) (*objs.Result[bool], error) {
	args := &objs.ApproveChatJoinRequestArgs{
		UserId: userId,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("approveChatJoinRequest", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*DeclineChatJoinRequest declines a request from the given user to join the chat.*/
func (bai *BotAPIInterface) DeclineChatJoinRequest(chatIdInt int, chatIdString string, userId int) (*objs.Result[bool], error) {
	args := &objs.DeclineChatJoinRequestArgs{
		UserId: userId,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("declineChatJoinRequest", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*SetChatPhoto sets the chat photo to given file.*/
func (bai *BotAPIInterface) SetChatPhoto(chatIdInt int, chatIdString string, file *os.File) (*objs.Result[bool], error) {
	args := &objs.SetChatPhotoArgs{}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	stats, er := file.Stat()
	if er != nil {
		return nil, er
	}
	args.Photo = "attach://" + stats.Name()
	res, err := bai.SendCustom("setChatPhoto", args, true, file)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*DeleteChatPhoto deletes chat photo.*/
func (bai *BotAPIInterface) DeleteChatPhoto(chatIdInt int, chatIdString string) (*objs.Result[bool], error) {
	args := &objs.DefaultChatArgs{}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("deleteChatPhoto", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*SetChatTitle sets the chat title.*/
func (bai *BotAPIInterface) SetChatTitle(chatIdInt int, chatIdString, title string) (*objs.Result[bool], error) {
	args := &objs.SetChatTitleArgs{
		Title: title,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("setChatTitle", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*SetChatDescription sets the chat description.*/
func (bai *BotAPIInterface) SetChatDescription(chatIdInt int, chatIdString, descriptions string) (*objs.Result[bool], error) {
	args := &objs.SetChatDescriptionArgs{
		Description: descriptions,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("setChatDescription", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*PinChatMessage pins the message in the chat.*/
func (bai *BotAPIInterface) PinChatMessage(chatIdInt int, chatIdString string, messageId int, disableNotification bool) (*objs.Result[bool], error) {
	args := &objs.PinChatMessageArgs{
		MessageId:           messageId,
		DisableNotification: disableNotification,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("pinChatMessage", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*UnpinChatMessage unpins the pinned message in the chat.*/
func (bai *BotAPIInterface) UnpinChatMessage(chatIdInt int, chatIdString string, messageId int) (*objs.Result[bool], error) {
	args := &objs.UnpinChatMessageArgs{
		MessageId: messageId,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("unpinChatMessage", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*UnpinAllChatMessages unpins all the pinned messages in the chat.*/
func (bai *BotAPIInterface) UnpinAllChatMessages(chatIdInt int, chatIdString string) (*objs.Result[bool], error) {
	args := &objs.DefaultChatArgs{}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("unpinAllChatMessages", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*LeaveChat, the bot will leave the chat if this method is called.*/
func (bai *BotAPIInterface) LeaveChat(chatIdInt int, chatIdString string) (*objs.Result[bool], error) {
	args := &objs.DefaultChatArgs{}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("leaveChat", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*GetChat : a Chat object containing the information of the chat will be returned*/
func (bai *BotAPIInterface) GetChat(chatIdInt int, chatIdString string) (*objs.Result[*objs.Chat], error) {
	args := &objs.DefaultChatArgs{}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("getChat", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[*objs.Chat]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*GetChatAdministrators returns an array of ChatMember containing the informations of the chat administrators.*/
func (bai *BotAPIInterface) GetChatAdministrators(chatIdInt int, chatIdString string) (*objs.Result[[]objs.ChatMemberOwner], error) {
	args := &objs.DefaultChatArgs{}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("getChatAdministrators", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[[]objs.ChatMemberOwner]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/* GetChatMemberCount returns the number of the memebrs of the chat.*/
func (bai *BotAPIInterface) GetChatMemberCount(chatIdInt int, chatIdString string) (*objs.Result[int], error) {
	args := &objs.DefaultChatArgs{}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("getChatMemberCount", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[int]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*GetChatMember returns the information of the member in a ChatMember object.*/
func (bai *BotAPIInterface) GetChatMember(chatIdInt int, chatIdString string, userId int) (*objs.Result[json.RawMessage], error) {
	args := &objs.GetChatMemberArgs{
		UserId: userId,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("getChatMember", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[json.RawMessage]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*SetChatStickerSet sets the sticker set of the chat.*/
func (bai *BotAPIInterface) SetChatStickerSet(chatIdInt int, chatIdString, stickerSetName string) (*objs.Result[bool], error) {
	args := &objs.SetChatStcikerSet{
		StickerSetName: stickerSetName,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("setChatStickerSet", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*DeleteChatStickerSet deletes the sticker set of the chat..*/
func (bai *BotAPIInterface) DeleteChatStickerSet(chatIdInt int, chatIdString string) (*objs.Result[bool], error) {
	args := &objs.DefaultChatArgs{}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("deleteChatStickerSet", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*AnswerCallbackQuery answers a callback query*/
func (bai *BotAPIInterface) AnswerCallbackQuery(callbackQueryId, text, url string, showAlert bool, CacheTime int) (*objs.Result[bool], error) {
	args := &objs.AnswerCallbackQueryArgs{
		CallbackQueyId: callbackQueryId,
		Text:           text,
		ShowAlert:      showAlert,
		URL:            url,
		CacheTime:      CacheTime,
	}
	res, err := bai.SendCustom("answerCallbackQuery", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*SetMyCommands sets the commands of the bot*/
func (bai *BotAPIInterface) SetMyCommands(commands []objs.BotCommand, scope objs.BotCommandScope, languageCode string) (*objs.Result[bool], error) {
	args := &objs.SetMyCommandsArgs{
		Commands: commands,
		MyCommandsDefault: objs.MyCommandsDefault{
			Scope:        scope,
			LanguageCode: languageCode,
		},
	}
	res, err := bai.SendCustom("setMyCommands", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*DeleteMyCommands deletes the commands of the bot*/
func (bai *BotAPIInterface) DeleteMyCommands(scope objs.BotCommandScope, languageCode string) (*objs.Result[bool], error) {
	args := &objs.MyCommandsDefault{
		Scope:        scope,
		LanguageCode: languageCode,
	}
	res, err := bai.SendCustom("deleteMyCommands", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*GetMyCommands gets the commands of the bot*/
func (bai *BotAPIInterface) GetMyCommands(scope objs.BotCommandScope, languageCode string) (*objs.Result[[]objs.BotCommand], error) {
	args := &objs.MyCommandsDefault{
		Scope:        scope,
		LanguageCode: languageCode,
	}
	res, err := bai.SendCustom("getMyCommands", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[[]objs.BotCommand]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*EditMessageText edits the text of the given message in the given chat.*/
func (bai *BotAPIInterface) EditMessageText(chatIdInt int, chatIdString string, messageId int, inlineMessageId, text, parseMode string, entities []objs.MessageEntity, disableWebPagePreview bool, replyMakrup *objs.InlineKeyboardMarkup) (*objs.Result[json.RawMessage], error) {
	args := &objs.EditMessageTextArgs{
		EditMessageDefaultArgs: objs.EditMessageDefaultArgs{
			MessageId:       messageId,
			InlineMessageId: inlineMessageId,
			ReplyMarkup:     replyMakrup,
		},
		Text:                  text,
		ParseMode:             parseMode,
		Entities:              entities,
		DisablewebpagePreview: disableWebPagePreview,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("editMessageText", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[json.RawMessage]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*EditMessageCaption edits the caption of the given message in the given chat.*/
func (bai *BotAPIInterface) EditMessageCaption(chatIdInt int, chatIdString string, messageId int, inlineMessageId, caption, parseMode string, captionEntities []objs.MessageEntity, replyMakrup *objs.InlineKeyboardMarkup) (*objs.Result[json.RawMessage], error) {
	args := &objs.EditMessageCaptionArgs{
		EditMessageDefaultArgs: objs.EditMessageDefaultArgs{
			MessageId:       messageId,
			InlineMessageId: inlineMessageId,
			ReplyMarkup:     replyMakrup,
		},
		Caption:         caption,
		ParseMode:       parseMode,
		CaptionEntities: captionEntities,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("editMessageCaption", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[json.RawMessage]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*EditMessageMedia edits the media of the given message in the given chat.*/
func (bai *BotAPIInterface) EditMessageMedia(chatIdInt int, chatIdString string, messageId int, inlineMessageId string, media objs.InputMedia, replyMakrup *objs.InlineKeyboardMarkup, file ...*os.File) (*objs.Result[json.RawMessage], error) {
	args := &objs.EditMessageMediaArgs{
		EditMessageDefaultArgs: objs.EditMessageDefaultArgs{
			MessageId:       messageId,
			InlineMessageId: inlineMessageId,
			ReplyMarkup:     replyMakrup,
		},
		Media: media,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("editMessageMedia", args, true, file...)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[json.RawMessage]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*EditMessagereplyMarkup edits the reply makrup of the given message in the given chat.*/
func (bai *BotAPIInterface) EditMessagereplyMarkup(chatIdInt int, chatIdString string, messageId int, inlineMessageId string, replyMakrup *objs.InlineKeyboardMarkup) (*objs.Result[json.RawMessage], error) {
	args := &objs.EditMessageReplyMakrupArgs{
		EditMessageDefaultArgs: objs.EditMessageDefaultArgs{
			MessageId:       messageId,
			InlineMessageId: inlineMessageId,
			ReplyMarkup:     replyMakrup,
		},
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("editMessageReplyMarkup", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[json.RawMessage]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*StopPoll stops the poll.*/
func (bai *BotAPIInterface) StopPoll(chatIdInt int, chatIdString string, messageId int, replyMakrup *objs.InlineKeyboardMarkup) (*objs.Result[*objs.Poll], error) {
	args := &objs.StopPollArgs{
		MessageId:   messageId,
		ReplyMarkup: replyMakrup,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("stopPoll", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[*objs.Poll]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*DeleteMessage deletes the given message int the given chat.*/
func (bai *BotAPIInterface) DeleteMessage(chatIdInt int, chatIdString string, messageId int) (*objs.Result[bool], error) {
	args := &objs.DeleteMessageArgs{
		MessageId: messageId,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("deleteMessage", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*SendSticker sends an sticker to the given chat id.*/
func (bai *BotAPIInterface) SendSticker(chatIdInt int, chatIdString, sticker, emoji string, disableNotif, allowSendingWithoutreply, protectContent bool, replyTo, messageThreadId int, replyMarkup objs.ReplyMarkup, file *os.File) (*objs.Result[*objs.Message], error) {
	args := &objs.SendStickerArgs{
		DefaultSendMethodsArguments: objs.DefaultSendMethodsArguments{
			DisableNotification:      disableNotif,
			AllowSendingWithoutReply: allowSendingWithoutreply,
			ReplyToMessageId:         replyTo,
			MessageThreadId:          messageThreadId,
			ReplyMarkup:              replyMarkup,
			ProtectContent:           protectContent,
		},
		Sticker: sticker,
		Emoji:   emoji,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("sendSticker", args, true, file)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[*objs.Message]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*GetStickerSet gets the sticker set by the given name*/
func (bai *BotAPIInterface) GetStickerSet(name string) (*objs.Result[*objs.StickerSet], error) {
	args := &objs.GetStickerSetArgs{
		Name: name,
	}
	res, err := bai.SendCustom("getStickerSet", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[*objs.StickerSet]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*UploadStickerFile uploads the given file as an sticker on the telegram servers.*/
func (bai *BotAPIInterface) UploadStickerFile(userId int, stickerFormat string, sticker *objs.InputSticker, file *os.File) (*objs.Result[*objs.File], error) {
	args := &objs.UploadStickerFileArgs{
		UserId:        userId,
		Sticker:       sticker,
		StickerFormat: stickerFormat,
	}
	res, err := bai.SendCustom("uploadStickerFile", args, true, file)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[*objs.File]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*CreateNewStickerSet creates a new sticker set with the given arguments*/
func (bai *BotAPIInterface) CreateNewStickerSet(userId int, name, title, StickerFormat, StickerType string, needsRepainting bool, stickers []*objs.InputSticker, files ...*os.File) (*objs.Result[bool], error) {
	args := &objs.CreateNewStickerSetArgs{
		UserId:          userId,
		Name:            name,
		Title:           title,
		Stickers:        stickers,
		StickerFormat:   StickerFormat,
		StickerType:     StickerType,
		NeedsRepainting: needsRepainting,
	}
	res, err := bai.SendCustom("createNewStickerSet", args, true, files...)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*AddStickerToSet adds a new sticker to the given set.*/
func (bai *BotAPIInterface) AddStickerToSet(userId int, name string, sticker *objs.InputSticker, file *os.File) (*objs.Result[bool], error) {
	args := &objs.AddStickerSetArgs{
		UserId:  userId,
		Name:    name,
		Sticker: sticker,
	}
	res, err := bai.SendCustom("addStickerToSet", args, true, file)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*SetStickerPositionInSet sets the position of a sticker in an sticker set*/
func (bai *BotAPIInterface) SetStickerPositionInSet(sticker string, position int) (*objs.Result[bool], error) {
	args := &objs.SetStickerPositionInSetArgs{
		Sticker:  sticker,
		Position: position,
	}
	res, err := bai.SendCustom("setStickerPositionInSet", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*DeleteStickerFromSet deletes the given sticker from a set created by the bot*/
func (bai *BotAPIInterface) DeleteStickerFromSet(sticker string) (*objs.Result[bool], error) {
	args := &objs.DeleteStickerFromSetArgs{
		Sticker: sticker,
	}
	res, err := bai.SendCustom("deleteStickerFromSet", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*SetStickerSetThumb sets the thumbnail for the given sticker*/
func (bai *BotAPIInterface) SetStickerSetThumb(name, thumb string, userId int, file *os.File) (*objs.Result[bool], error) {
	args := &objs.SetStickerSetThumbnailArgs{
		Name:   name,
		Thumb:  thumb,
		UserId: userId,
	}
	res, err := bai.SendCustom("setStickerSetThumb", args, true, file)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

// DeleteStickerSet deletes a sticker set that was created by the bot. Returns True on success.
func (bai *BotAPIInterface) DeleteStickerSet(name string) (*objs.Result[bool], error) {
	args := &objs.DeleteStickerSetArgs{
		Name: name,
	}
	res, err := bai.SendCustom("deleteStickerSet", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

// SetStickerSetTitle sets the title of a created sticker set. Returns True on success.
func (bai *BotAPIInterface) SetStickerSetTitle(name, title string) (*objs.Result[bool], error) {
	args := &objs.SetStickerSetTitleArgs{
		Name:  name,
		Title: title,
	}
	res, err := bai.SendCustom("setStickerSetTitle", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

// SetStickerEmojiList changes the list of emoji assigned to a regular or custom emoji sticker. The sticker must belong to a sticker set created by the bot. Returns True on success.
func (bai *BotAPIInterface) SetStickerEmojiList(sticker string, emojiLst []string) (*objs.Result[bool], error) {
	args := &objs.SetStickerEmojiListArgs{
		Sticker:   sticker,
		EmojiList: emojiLst,
	}
	res, err := bai.SendCustom("setStickerEmojiList", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

// SetStickerKeywords changes search keywords assigned to a regular or custom emoji sticker. The sticker must belong to a sticker set created by the bot. Returns True on success.
func (bai *BotAPIInterface) SetStickerKeywords(sticker string, keywords []string) (*objs.Result[bool], error) {
	args := &objs.SetStickerKeywordsArgs{
		Sticker:   sticker,
		Keywoards: keywords,
	}
	res, err := bai.SendCustom("setStickerKeywords", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

// SetStickerMaskPosition changes the mask position of a mask sticker. The sticker must belong to a sticker set that was created by the bot. Returns True on success.
func (bai *BotAPIInterface) SetStickerMaskPosition(sticker string, maskPosition *objs.MaskPosition) (*objs.Result[bool], error) {
	args := &objs.SetStickerMaskPositionArgs{
		Sticker:      sticker,
		MaskPosition: maskPosition,
	}
	res, err := bai.SendCustom("setStickerMaskPosition", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*AnswerInlineQuery answers an inline query with the given parameters*/
func (bai *BotAPIInterface) AnswerInlineQuery(inlineQueryId string, results []objs.InlineQueryResult, cacheTime int, isPersonal bool, nextOffset string, button *objs.InlineQueryResultsButton) (*objs.Result[bool], error) {
	args := &objs.AnswerInlineQueryArgs{
		InlineQueryId: inlineQueryId,
		Results:       results,
		CacheTime:     cacheTime,
		IsPersonal:    isPersonal,
		NextOffset:    nextOffset,
		Button:        button,
	}
	res, err := bai.SendCustom("answerInlineQuery", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*SendInvoice sends an invoice*/
func (bai *BotAPIInterface) SendInvoice(chatIdInt int, chatIdString, title, description, payload, providerToken, currency string, prices []objs.LabeledPrice, maxTipAmount int, suggestedTipAmounts []int, startParameter, providerData, photoURL string, photoSize, photoWidth, photoHeight int, needName, needPhoneNumber, needEmail, needSippingAddress, sendPhoneNumberToProvider, sendEmailToProvider, isFlexible, disableNotif bool, replyToMessageId, messageThreadId int, allowSendingWithoutReply bool, replyMarkup objs.InlineKeyboardMarkup) (*objs.Result[*objs.Message], error) {
	args := &objs.SendInvoiceArgs{
		DefaultSendMethodsArguments: objs.DefaultSendMethodsArguments{
			DisableNotification:      disableNotif,
			AllowSendingWithoutReply: allowSendingWithoutReply,
			ReplyToMessageId:         replyToMessageId,
			ReplyMarkup:              &replyMarkup,
			MessageThreadId:          messageThreadId,
		},
		Title:                     title,
		Description:               description,
		Payload:                   payload,
		ProviderToken:             providerToken,
		Currency:                  currency,
		Prices:                    prices,
		MaxTipAmount:              maxTipAmount,
		SuggestedTipAmounts:       suggestedTipAmounts,
		StartParameter:            startParameter,
		ProviderData:              providerData,
		PhotoURL:                  photoURL,
		PhotoSize:                 photoSize,
		PhotoWidth:                photoWidth,
		PhotoHeight:               photoHeight,
		NeedName:                  needName,
		NeedPhoneNumber:           needPhoneNumber,
		NeedEmail:                 needEmail,
		NeedShippingAddress:       needSippingAddress,
		SendPhoneNumberToProvider: sendPhoneNumberToProvider,
		SendEmailToProvider:       sendEmailToProvider,
		IsFlexible:                isFlexible,
	}
	args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	res, err := bai.SendCustom("sendInvoice", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[*objs.Message]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*CreateInvoiceLink sends an invoice*/
func (bai *BotAPIInterface) CreateInvoiceLink(title, description, payload, providerToken, currency string, prices []objs.LabeledPrice, maxTipAmount int, suggestedTipAmounts []int, providerData, photoURL string, photoSize, photoWidth, photoHeight int, needName, needPhoneNumber, needEmail, needSippingAddress, sendPhoneNumberToProvider, sendEmailToProvider, isFlexible bool) (*objs.Result[string], error) {
	args := &objs.SendInvoiceArgs{
		Title:                     title,
		Description:               description,
		Payload:                   payload,
		ProviderToken:             providerToken,
		Currency:                  currency,
		Prices:                    prices,
		MaxTipAmount:              maxTipAmount,
		SuggestedTipAmounts:       suggestedTipAmounts,
		ProviderData:              providerData,
		PhotoURL:                  photoURL,
		PhotoSize:                 photoSize,
		PhotoWidth:                photoWidth,
		PhotoHeight:               photoHeight,
		NeedName:                  needName,
		NeedPhoneNumber:           needPhoneNumber,
		NeedEmail:                 needEmail,
		NeedShippingAddress:       needSippingAddress,
		SendPhoneNumberToProvider: sendPhoneNumberToProvider,
		SendEmailToProvider:       sendEmailToProvider,
		IsFlexible:                isFlexible,
	}
	res, err := bai.SendCustom("createInvoiceLink", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[string]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*AnswerShippingQuery answers a shipping query*/
func (bai *BotAPIInterface) AnswerShippingQuery(shippingQueryId string, ok bool, shippingOptions []objs.ShippingOption, errorMessage string) (*objs.Result[bool], error) {
	args := &objs.AnswerShippingQueryArgs{
		ShippingQueryId: shippingQueryId,
		OK:              ok,
		ShippingOptions: shippingOptions,
		ErrorMessage:    errorMessage,
	}
	res, err := bai.SendCustom("answerShippingQuery", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*AnswerPreCheckoutQuery answers a pre checkout query*/
func (bai *BotAPIInterface) AnswerPreCheckoutQuery(preCheckoutQueryId string, ok bool, errorMessage string) (*objs.Result[bool], error) {
	args := &objs.AnswerPreCheckoutQueryArgs{
		PreCheckoutQueryId: preCheckoutQueryId,
		Ok:                 ok,
		ErrorMessage:       errorMessage,
	}
	res, err := bai.SendCustom("answerPreCheckoutQuery", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*
CopyMessage copies a message from a user or channel and sends it to a user or channel. If the source or destination (or both) of the forwarded message is a channel, only string chat ids should be given to the function, and if it is user only int chat ids should be given.
"chatId", "fromChatId" and "messageId" arguments are required. other arguments are optional for bot api.
*/
func (bai *BotAPIInterface) CopyMessage(chatIdInt, fromChatIdInt int, chatIdString, fromChatIdString string, messageId int, disableNotif bool, caption, parseMode string, replyTo int, allowSendingWihtoutReply, ProtectContent bool, replyMarkUp objs.ReplyMarkup, captionEntities []objs.MessageEntity) (*objs.Result[*objs.Message], error) {
	if (chatIdInt != 0 && chatIdString != "") && (fromChatIdInt != 0 && fromChatIdString != "") {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) && bai.isChatIdOk(fromChatIdInt, fromChatIdString) {
		fm := objs.ForwardMessageArgs{
			DisableNotification: disableNotif,
			MessageId:           messageId,
			ProtectContent:      ProtectContent,
		}
		fm.ChatId = bai.fixChatId(chatIdInt, chatIdString)
		fm.FromChatId = bai.fixChatId(fromChatIdInt, fromChatIdString)
		cp := &objs.CopyMessageArgs{
			ForwardMessageArgs:       fm,
			Caption:                  caption,
			ParseMode:                parseMode,
			AllowSendingWithoutReply: allowSendingWihtoutReply,
			ReplyMarkup:              replyMarkUp,
			CaptionEntities:          captionEntities,
		}
		if replyTo != 0 {
			cp.ReplyToMessageId = replyTo
		}
		res, err := bai.SendCustom("copyMessage", cp, false, nil, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[*objs.Message]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString or fromChatIdInt or fromChatIdString", MethodName: "copyMessage"}
	}
}

/*SetPassportDataErrors sets passport data errors*/
func (bai *BotAPIInterface) SetPassportDataErrors(userId int, errors []objs.PassportElementError) (*objs.Result[bool], error) {
	args := &objs.SetPassportDataErrorsArgs{
		UserId: userId, Errors: errors,
	}
	res, err := bai.SendCustom("setPassportDataErrors", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*SendGame sends a game*/
func (bai *BotAPIInterface) SendGame(chatId int, gameShortName string, disableNotif bool, replyTo int, allowSendingWithoutReply bool, replyMarkup objs.ReplyMarkup) (*objs.Result[*objs.Message], error) {
	args := &objs.SendGameArgs{
		DefaultSendMethodsArguments: objs.DefaultSendMethodsArguments{
			ReplyToMessageId:         replyTo,
			DisableNotification:      disableNotif,
			ReplyMarkup:              replyMarkup,
			AllowSendingWithoutReply: allowSendingWithoutReply,
		},
		GameShortName: gameShortName,
	}
	bt, _ := json.Marshal(chatId)
	args.ChatId = bt
	res, err := bai.SendCustom("sendGame", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[*objs.Message]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*SetGameScore sets the game high score*/
func (bai *BotAPIInterface) SetGameScore(userId, score int, force, disableEditMessage bool, chatId, messageId int, inlineMessageId string) (*objs.Result[json.RawMessage], error) {
	args := &objs.SetGameScoreArgs{
		UserId:             userId,
		Score:              score,
		Force:              force,
		DisableEditMessage: disableEditMessage,
		ChatId:             chatId,
		MessageId:          messageId,
		InlineMessageId:    inlineMessageId,
	}
	res, err := bai.SendCustom("setGameScore", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[json.RawMessage]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*GetGameHighScores gets the high scores of the user*/
func (bai *BotAPIInterface) GetGameHighScores(userId, chatId, messageId int, inlineMessageId string) (*objs.Result[[]*objs.GameHighScore], error) {
	args := &objs.GetGameHighScoresArgs{
		UserId:          userId,
		ChatId:          chatId,
		MessageId:       messageId,
		InlineMessageId: inlineMessageId,
	}
	res, err := bai.SendCustom("getGameHighScores", args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[[]*objs.GameHighScore]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*GetWebhookInfo returns the web hook info of the bot.*/
func (bai *BotAPIInterface) GetWebhookInfo() (*objs.Result[*objs.WebhookInfo], error) {
	res, err := bai.SendCustom("getWebhookInfo", nil, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[*objs.WebhookInfo]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*SetWebhook sets a webhook for the bot.*/
func (bai *BotAPIInterface) SetWebhook(url, ip string, maxCnc int, allowedUpdates []string, dropPendingUpdates bool, keyFile *os.File) (*objs.Result[bool], error) {
	args := objs.SetWebhookArgs{
		URL:                url,
		IPAddress:          ip,
		MaxConnections:     maxCnc,
		AllowedUpdates:     allowedUpdates,
		DropPendingUpdates: dropPendingUpdates,
	}
	if keyFile != nil {
		stat, errs := keyFile.Stat()
		if errs != nil {
			return nil, errs
		}
		args.Certificate = "attach://" + stat.Name()
	}
	res, err := bai.SendCustom("setWebhook", &args, keyFile != nil, keyFile)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*DeleteWebhook deletes the webhook for this bot*/
func (bai *BotAPIInterface) DeleteWebhook(dropPendingUpdates bool) (*objs.Result[bool], error) {
	args := objs.DeleteWebhookArgs{
		DropPendingUpdates: dropPendingUpdates,
	}
	res, err := bai.SendCustom("deleteWebhook", &args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*GetCustomEmojiStickers returns the information of the speicified stickers by id*/
func (bai *BotAPIInterface) GetCustomEmojiStickers(customEmojiIds []string) (*objs.Result[[]*objs.Sticker], error) {
	args := objs.GetCustomEmojiStickersArgs{
		CustomEmojiIds: customEmojiIds,
	}
	res, err := bai.SendCustom("getCustomEmojiStickers", &args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[[]*objs.Sticker]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*AnswerWebAppQuery answers a web app query*/
func (bai *BotAPIInterface) AnswerWebAppQuery(webAppQueryId string, result objs.InlineQueryResult) (*objs.SentWebAppMessage, error) {
	args := objs.AnswerWebAppQueryArgs{
		WebAppQueryId: webAppQueryId,
		Result:        result,
	}
	res, err := bai.SendCustom("answerWebAppQuery", &args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.SentWebAppMessage{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*GetChatMenuButton gets the menu button for the given chat*/
func (bai *BotAPIInterface) GetChatMenuButton(chatId int64) (*objs.Result[*objs.MenuButton], error) {
	args := objs.ChatMenuButtonArgs{
		ChatId: chatId,
	}
	res, err := bai.SendCustom("getChatMenuButton", &args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[*objs.MenuButton]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*SetChatMenuButton sets the menu button for the given chat*/
func (bai *BotAPIInterface) SetChatMenuButton(chatId int64, menuButton *objs.MenuButton) (*objs.Result[bool], error) {
	args := objs.ChatMenuButtonArgs{
		ChatId:     chatId,
		MenuButton: menuButton,
	}
	res, err := bai.SendCustom("setChatMenuButton", &args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*GetForumTopicIconStickers returns custom emoji stickers, which can be used as a forum topic icon by any user.*/
func (bai *BotAPIInterface) GetForumTopicIconStickers() (*objs.Result[[]*objs.Sticker], error) {
	res, err := bai.SendCustom("getForumTopicIconStickers", nil, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[[]*objs.Sticker]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*CreateForumTopic creates a topic in a forum supergroup chat. The bot must be an administrator in the chat for this to work and must have the can_manage_topics administrator rights*/
func (bai *BotAPIInterface) CreateForumTopic(chatIdInt int, chatIdString, name, iconCustomEmojiId string, iconColor int) (*objs.Result[*objs.ForumTopic], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.CreateForumTopicArgs{
			ChatId:            bai.fixChatId(chatIdInt, chatIdString),
			Name:              name,
			IconColor:         iconColor,
			IconCustomEmojiId: iconCustomEmojiId,
		}
		args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
		res, err := bai.SendCustom("createForumTopic", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[*objs.ForumTopic]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "createForumTopic"}
	}
}

/*EditForumTopic edits name and icon of a topic in a forum supergroup chat. The bot must be an administrator in the chat for this to work and must have can_manage_topics administrator rights, unless it is the creator of the topic*/
func (bai *BotAPIInterface) EditForumTopic(chatIdInt int, chatIdString, name, iconCustomEmojiId string, messageThreadId int) (*objs.Result[bool], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.EditForumTopicArgs{
			ChatId:            bai.fixChatId(chatIdInt, chatIdString),
			Name:              name,
			IconCustomEmojiId: iconCustomEmojiId,
			MessageThreadId:   messageThreadId,
		}
		args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
		res, err := bai.SendCustom("editForumTopic", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[bool]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "editForumTopic"}
	}
}

/*CloseForumTopic closes an open topic in a forum supergroup chat. The bot must be an administrator in the chat for this to work and must have the can_manage_topics administrator rights, unless it is the creator of the topic*/
func (bai *BotAPIInterface) CloseForumTopic(chatIdInt int, chatIdString string, messageThreadId int) (*objs.Result[bool], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.CloseForumTopicArgs{
			ChatId:          bai.fixChatId(chatIdInt, chatIdString),
			MessageThreadId: messageThreadId,
		}
		args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
		res, err := bai.SendCustom("closeForumTopic", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[bool]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "closeForumTopic"}
	}
}

/*ReopenForumTopic reopens a closed topic in a forum supergroup chat. The bot must be an administrator in the chat for this to work and must have the can_manage_topics administrator rights, unless it is the creator of the topic*/
func (bai *BotAPIInterface) ReopenForumTopic(chatIdInt int, chatIdString string, messageThreadId int) (*objs.Result[bool], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.ReopenForumTopicArgs{
			CloseForumTopicArgs: &objs.CloseForumTopicArgs{
				ChatId:          bai.fixChatId(chatIdInt, chatIdString),
				MessageThreadId: messageThreadId,
			},
		}
		args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
		res, err := bai.SendCustom("reopenForumTopic", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[bool]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "reopenForumTopic"}
	}
}

/*DeleteForumTopic deletes a forum topic along with all its messages in a forum supergroup chat. The bot must be an administrator in the chat for this to work and must have the can_delete_messages administrator rights*/
func (bai *BotAPIInterface) DeleteForumTopic(chatIdInt int, chatIdString string, messageThreadId int) (*objs.Result[bool], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.DeleteForumTopicArgs{
			CloseForumTopicArgs: &objs.CloseForumTopicArgs{
				ChatId:          bai.fixChatId(chatIdInt, chatIdString),
				MessageThreadId: messageThreadId,
			},
		}
		args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
		res, err := bai.SendCustom("deleteForumTopic", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[bool]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "deleteForumTopic"}
	}
}

/*UnpinAllForumTopicMessages clears the list of pinned messages in a forum topic. The bot must be an administrator in the chat for this to work and must have the can_pin_messages administrator right in the supergroup*/
func (bai *BotAPIInterface) UnpinAllForumTopicMessages(chatIdInt int, chatIdString string, messageThreadId int) (*objs.Result[bool], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.UnpinAllForumTopicMessages{
			CloseForumTopicArgs: &objs.CloseForumTopicArgs{
				ChatId:          bai.fixChatId(chatIdInt, chatIdString),
				MessageThreadId: messageThreadId,
			},
		}
		args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
		res, err := bai.SendCustom("unpinAllForumTopicMessages", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[bool]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "unpinAllForumTopicMessages"}
	}
}

/*EditGeneralForumTopic edits the name of the 'General' topic in a forum supergroup chat. The bot must be an administrator in the chat for this to work and must have can_manage_topics administrator rights*/
func (bai *BotAPIInterface) EditGeneralForumTopic(chatIdInt int, chatIdString, name string) (*objs.Result[bool], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.EditGeneralForumTopic{
			ChatId: bai.fixChatId(chatIdInt, chatIdString),
			Name:   name,
		}
		args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
		res, err := bai.SendCustom("editGeneralForumTopic", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[bool]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "editGeneralForumTopic"}
	}
}

/*CloseGeneralForumTopic closes an open 'General' topic in a forum supergroup chat. The bot must be an administrator in the chat for this to work and must have the can_manage_topics administrator rights*/
func (bai *BotAPIInterface) CloseGeneralForumTopic(chatIdInt int, chatIdString string) (*objs.Result[bool], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.CloseGeneralForumTopic{
			ChatId: bai.fixChatId(chatIdInt, chatIdString),
		}
		args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
		res, err := bai.SendCustom("closeGeneralForumTopic", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[bool]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "closeGeneralForumTopic"}
	}
}

/*ReopenGeneralForumTopic reopens a closed 'General' topic in a forum supergroup chat. The bot must be an administrator in the chat for this to work and must have the can_manage_topics administrator rights. The topic will be automatically unhidden if it was hidden.*/
func (bai *BotAPIInterface) ReopenGeneralForumTopic(chatIdInt int, chatIdString string) (*objs.Result[bool], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.ReopenGeneralForumTopic{
			CloseGeneralForumTopic: &objs.CloseGeneralForumTopic{
				ChatId: bai.fixChatId(chatIdInt, chatIdString),
			},
		}
		args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
		res, err := bai.SendCustom("reopenGeneralForumTopic", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[bool]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "reopenGeneralForumTopic"}
	}
}

/*HideGeneralForumTopic hides the 'General' topic in a forum supergroup chat. The bot must be an administrator in the chat for this to work and must have the can_manage_topics administrator rights. The topic will be automatically closed if it was open.*/
func (bai *BotAPIInterface) HideGeneralForumTopic(chatIdInt int, chatIdString string) (*objs.Result[bool], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.HideGeneralForumTopic{
			CloseGeneralForumTopic: &objs.CloseGeneralForumTopic{
				ChatId: bai.fixChatId(chatIdInt, chatIdString),
			},
		}
		args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
		res, err := bai.SendCustom("hideGeneralForumTopic", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[bool]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "hideGeneralForumTopic"}
	}
}

/*UnhideGeneralForumTopic unhides the 'General' topic in a forum supergroup chat. The bot must be an administrator in the chat for this to work and must have the can_manage_topics administrator rights.*/
func (bai *BotAPIInterface) UnhideGeneralForumTopic(chatIdInt int, chatIdString string) (*objs.Result[bool], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.UnhideGeneralForumTopic{
			CloseGeneralForumTopic: &objs.CloseGeneralForumTopic{
				ChatId: bai.fixChatId(chatIdInt, chatIdString),
			},
		}
		args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
		res, err := bai.SendCustom("unhideGeneralForumTopic", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[bool]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "unhideGeneralForumTopic"}
	}
}

/*UnpinAllGeneralForumTopicMessages clears the list of pinned messages in a General forum topic. The bot must be an administrator in the chat for this to work and must have the can_pin_messages administrator right in the supergroup.*/
func (bai *BotAPIInterface) UnpinAllGeneralForumTopicMessages(chatIdInt int, chatIdString string) (*objs.Result[bool], error) {
	if chatIdInt != 0 && chatIdString != "" {
		return nil, &errs.ChatIdProblem{}
	}
	if bai.isChatIdOk(chatIdInt, chatIdString) {
		args := &objs.UnpinAllGeneralForumTopicMessages{
			CloseGeneralForumTopic: &objs.CloseGeneralForumTopic{
				ChatId: bai.fixChatId(chatIdInt, chatIdString),
			},
		}
		args.ChatId = bai.fixChatId(chatIdInt, chatIdString)
		res, err := bai.SendCustom("unpinAllGeneralForumTopicMessages", args, false, nil)
		if err != nil {
			return nil, err
		}
		msg := &objs.Result[bool]{}
		err3 := json.Unmarshal(res, msg)
		if err3 != nil {
			return nil, err3
		}
		return msg, nil
	} else {
		return nil, &errs.RequiredArgumentError{ArgName: "chatIdInt or chatIdString", MethodName: "unpinAllGeneralForumTopicMessages"}
	}
}

// SetMyDescription changes the bot's description, which is shown in the chat with the bot if the chat is empty. Returns True on success.
func (bai *BotAPIInterface) SetMyDescription(description, languageCode string) (*objs.Result[bool], error) {
	args := objs.SetMyDescriptionArgs{
		Description:  description,
		LanguageCode: languageCode,
	}
	res, err := bai.SendCustom("setMyDescription", &args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

// SetMyShortDescription changes the bot's short description, which is shown on the bot's profile page and is sent together with the link when users share the bot. Returns True on success.
func (bai *BotAPIInterface) SetMyShortDescription(description, languageCode string) (*objs.Result[bool], error) {
	args := objs.SetMyShortDescriptionArgs{
		ShortDescription: description,
		LanguageCode:     languageCode,
	}
	res, err := bai.SendCustom("setMyShortDescription", &args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

// GetMyDescription gets the current bot description for the given user language. Returns BotDescription on success.
func (bai *BotAPIInterface) GetMyDescription(languageCode string) (*objs.Result[*objs.BotDescription], error) {
	args := objs.GetMyDescriptionArgs{
		LanguageCode: languageCode,
	}
	res, err := bai.SendCustom("getMyDescription", &args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[*objs.BotDescription]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

// GetMyShortDescription gets the current bot short description for the given user language. Returns BotShortDescription on success.
func (bai *BotAPIInterface) GetMyShortDescription(languageCode string) (*objs.Result[*objs.BotShortDescription], error) {
	args := objs.GetMyDescriptionArgs{
		LanguageCode: languageCode,
	}
	res, err := bai.SendCustom("getMyShortDescription", &args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[*objs.BotShortDescription]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

// SetMyName changes the bot's name. Returns True on success.
func (bai *BotAPIInterface) SetMyName(name, languageCode string) (*objs.Result[bool], error) {
	args := objs.SetMyNameArgs{
		Name:         name,
		LanguageCode: languageCode,
	}
	res, err := bai.SendCustom("setMyName", &args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[bool]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

// GetMyName gets the current bot name for the given user language. Returns BotName on success.
func (bai *BotAPIInterface) GetMyName(languageCode string) (*objs.Result[*objs.BotName], error) {
	args := objs.GetMyNameArgs{
		LanguageCode: languageCode,
	}
	res, err := bai.SendCustom("getMyName", &args, false, nil)
	if err != nil {
		return nil, err
	}
	msg := &objs.Result[*objs.BotName]{}
	err3 := json.Unmarshal(res, msg)
	if err3 != nil {
		return nil, err3
	}
	return msg, nil
}

/*SendCustom calls the given method on api server with the given arguments. "MP" options indicates that the request should be made in multipart/formdata form. If this method sends a file to the api server the "MP" option should be true*/
func (bai *BotAPIInterface) SendCustom(methodName string, args objs.MethodArguments, MP bool, files ...*os.File) ([]byte, error) {
	start := time.Now().UnixMicro()
	cl := httpSenderClient{botApi: bai.botConfigs.BotAPI, apiKey: bai.botConfigs.APIKey}
	var res []byte
	var err2 error
	if MP {
		res, err2 = cl.sendHttpReqMultiPart(methodName, args, files...)
	} else {
		res, err2 = cl.sendHttpReqJson(methodName, args)
	}
	done := time.Now().UnixMicro()
	if err2 != nil {
		bai.logger.Log(methodName, "\t\t\t", "Error  ", strconv.FormatInt((done-start), 10)+"µs", logger.BOLD+logger.OKBLUE, logger.FAIL, "")
		return nil, err2
	}
	bai.logger.Log(methodName, "\t\t\t", "Success", strconv.FormatInt((done-start), 10)+"µs", logger.BOLD+logger.OKBLUE, logger.OKGREEN, "")
	return bai.preParseResult(res, methodName)
}

func (bai *BotAPIInterface) fixTheDefaultArguments(chatIdInt, reply_to_message_id, messageThreadId int, chatIdString string, disable_notification, allow_sending_without_reply, ProtectContent bool, reply_markup objs.ReplyMarkup) objs.DefaultSendMethodsArguments {
	def := objs.DefaultSendMethodsArguments{
		DisableNotification:      disable_notification,
		AllowSendingWithoutReply: allow_sending_without_reply,
		ProtectContent:           ProtectContent,
		ReplyToMessageId:         reply_to_message_id,
		ReplyMarkup:              reply_markup,
		MessageThreadId:          messageThreadId,
	}
	def.ChatId = bai.fixChatId(chatIdInt, chatIdString)
	return def
}

func (bai *BotAPIInterface) preParseResult(res []byte, method string) ([]byte, error) {
	def := &objs.Result[json.RawMessage]{}
	err := json.Unmarshal(res, def)
	if err != nil {
		return nil, err
	}
	if !def.Ok {
		fr := &objs.FailureResult{}
		err := json.Unmarshal(res, fr)
		if err != nil {
			return nil, err
		}
		return nil, &errs.MethodNotSentError{Method: method, Reason: "server returned false ok filed", FailureResult: fr}
	}
	return res, nil
}

func (bai *BotAPIInterface) fixChatId(chatIdInt int, chatIdString string) []byte {
	if chatIdInt == 0 {
		if !strings.HasPrefix(chatIdString, "@") {
			chatIdString = "@" + chatIdString
		}
		bt, _ := json.Marshal(chatIdString)
		return bt
	} else {
		bt, _ := json.Marshal(chatIdInt)
		return bt
	}
}

/*
CreateInterface returns an iterface to communicate with the bot api.
If the updateFrequency argument is not nil, the update routine begins automtically
*/
func CreateInterface(botCfg *cfgs.BotConfigs, botLogger *logger.BotLogger) (*BotAPIInterface, error) {
	if interfaceCreated {
		return nil, &errs.BotInterfaceAlreadyCreated{}
	}
	interfaceCreated = true
	ch := make(chan *objs.Update)
	ch3 := make(chan *objs.ChatUpdate)
	temp := &BotAPIInterface{
		botConfigs:         botCfg,
		updateChannel:      &ch,
		chatUpadateChannel: &ch3,
		updateParser:       parser.CreateUpdateParser(&ch, &ch3, botCfg, botLogger),
		logger:             botLogger,
	}
	return temp, nil
}
