package retailassistant

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	langEnglish = "en"
	langHindi   = "hi"
)

var (
	stopCommand    = regexp.MustCompile(`(?i)^\s*(stop|unsubscribe|बंद)\s*$`)
	startCommand   = regexp.MustCompile(`(?i)^\s*(start|शुरू)\s*$`)
	hindiCommand   = regexp.MustCompile(`(?i)^\s*(hindi|हिंदी|हिन्दी)\s*$`)
	englishCommand = regexp.MustCompile(`(?i)^\s*(english|अंग्रेज़ी|अंग्रेजी)\s*$`)
)

// The module's WhatsApp wording, by language. Model-translated Hindi: a native
// speaker has not reviewed it yet (docs/retail-assistant.md).
var phrases = map[string]map[string]string{
	"linked": {
		langEnglish: "This number is now linked to %s. Send a supplier bill or a payment proof here and it will wait for review in your dashboard.",
		langHindi:   "यह नंबर अब %s से जुड़ गया है। सप्लायर का बिल या पेमेंट का सबूत यहाँ भेजिए। वह आपके डैशबोर्ड में जाँच के लिए रुकेगा।",
	},
	"not_linked": {
		langEnglish: "This number is not linked to a store yet, so nothing you sent was kept. Ask your store's owner to add this number under Retail assistant, then send the code they give you.",
		langHindi:   "यह नंबर अभी किसी दुकान से नहीं जुड़ा है, इसलिए आपका भेजा हुआ नहीं रखा गया। दुकान के मालिक से कहिए कि वे यह नंबर Retail assistant में जोड़ें, फिर उनका दिया हुआ कोड भेजिए।",
	},
	"several_stores": {
		langEnglish: "This number works for several stores, so nothing you sent was kept. Reply STORE and the store's name, then send it again: %s",
		langHindi:   "यह नंबर कई दुकानों से जुड़ा है, इसलिए आपका भेजा हुआ नहीं रखा गया। STORE और दुकान का नाम लिखकर भेजिए, फिर दोबारा भेजिए: %s",
	},
	"store_chosen": {
		langEnglish: "What you send now goes to %s.",
		langHindi:   "अब आप जो भेजेंगे वह %s में जाएगा।",
	},
	"store_unknown": {
		langEnglish: "This number is not linked to a store called %s.",
		langHindi:   "यह नंबर %s नाम की किसी दुकान से नहीं जुड़ा है।",
	},
	"send_a_file": {
		langEnglish: "Send a supplier bill or a payment proof as a file or a photo, and it will wait for review in your dashboard.",
		langHindi:   "सप्लायर का बिल या पेमेंट का सबूत फ़ाइल या फ़ोटो में भेजिए। वह आपके डैशबोर्ड में जाँच के लिए रुकेगा।",
	},
	"received": {
		langEnglish: "Got %s. It is waiting for review in your dashboard.",
		langHindi:   "%s मिल गया। वह आपके डैशबोर्ड में जाँच के लिए रुका है।",
	},
	"your_file": {
		langEnglish: "your file",
		langHindi:   "आपकी फ़ाइल",
	},
	"posted_invoice": {
		langEnglish: "A bill from %s is in your books. You now owe %s %s.",
		langHindi:   "%s का बिल आपके खाते में दर्ज हो गया। अब आपको %s %s देना है।",
	},
	"posted_credit": {
		langEnglish: "A credit note from %s is in your books. You now owe %s %s.",
		langHindi:   "%s का क्रेडिट नोट आपके खाते में दर्ज हो गया। अब आपको %s %s देना है।",
	},
	"posted_payment": {
		langEnglish: "An unverified payment from %s is in your books. You now owe %s %s.",
		langHindi:   "%s का बिना जाँचा पेमेंट आपके खाते में दर्ज हो गया। अब आपको %s %s देना है।",
	},
	"stopped": {
		langEnglish: "You will not get confirmations here any more. You can still send bills. Reply START to get them again.",
		langHindi:   "अब आपको यहाँ पुष्टि के संदेश नहीं आएँगे। आप बिल भेजते रह सकते हैं। दोबारा पाने के लिए START भेजिए।",
	},
	"started": {
		langEnglish: "You will get confirmations here again. Reply STOP to end them.",
		langHindi:   "अब आपको यहाँ फिर से पुष्टि के संदेश आएँगे। बंद करने के लिए STOP भेजिए।",
	},
	"language_set": {
		langEnglish: "I will write to you in English now. Reply HINDI for Hindi.",
		langHindi:   "अब मैं आपको हिंदी में लिखूँगा। अंग्रेज़ी के लिए ENGLISH भेजिए।",
	},
}

// phrase is one message in a language, falling back to English so an unknown
// language can never leave somebody with no message.
func phrase(lang, key string, args ...any) string {
	entry := phrases[key]
	format, ok := entry[lang]
	if !ok {
		format = entry[langEnglish]
	}
	return fmt.Sprintf(format, args...)
}

// inBoth is for somebody whose language nobody knows yet.
func inBoth(key string, args ...any) string {
	return phrase(langHindi, key, args...) + "\n\n" + phrase(langEnglish, key, args...)
}

// templateText makes a message fit a template's variable, which Meta refuses if
// it holds a newline, a tab or a long run of spaces. A filename can hold any.
func templateText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
