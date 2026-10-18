package probe

import "strings"

// ISO 639-2/T to 639-2/B aliases, plus common two-letter codes. ffprobe is
// inconsistent about which form it reports, and config files are written by
// humans, so everything is normalized to 639-2/B before comparison.
var langAliases = map[string]string{
	// two-letter
	"en": "eng", "fr": "fre", "de": "ger", "es": "spa", "it": "ita",
	"ja": "jpn", "ko": "kor", "zh": "chi", "ru": "rus", "pt": "por",
	"nl": "dut", "sv": "swe", "no": "nor", "da": "dan", "fi": "fin",
	"pl": "pol", "cs": "cze", "hu": "hun", "ar": "ara", "he": "heb",
	"hi": "hin", "th": "tha", "tr": "tur", "uk": "ukr", "vi": "vie",
	"el": "gre", "ro": "rum", "bg": "bul", "hr": "hrv", "sr": "srp",
	"sk": "slo", "sl": "slv", "et": "est", "lv": "lav", "lt": "lit",
	"is": "ice", "id": "ind", "ms": "may", "tl": "tgl", "fa": "per",
	"ca": "cat",
	// 639-2/T variants
	"fra": "fre", "deu": "ger", "nld": "dut", "ces": "cze", "ron": "rum",
	"ell": "gre", "zho": "chi", "cmn": "chi", "msa": "may", "fas": "per",
	"kat": "geo", "isl": "ice", "mkd": "mac", "mya": "bur", "sqi": "alb",
	"hye": "arm", "eus": "baq", "bod": "tib", "cym": "wel", "gle": "iri",
	"gla": "gla", "slk": "slo", "srp": "srp", "hrv": "hrv", "bos": "bos",
	"lav": "lav", "lit": "lit", "est": "est", "mar": "mar", "ben": "ben",
	"tam": "tam", "tel": "tel", "kan": "kan", "mal": "mal", "pan": "pan",
	"guj": "guj", "urd": "urd", "nep": "nep", "sin": "sin", "khm": "khm",
	"lao": "lao", "mon": "mon", "kaz": "kaz", "uzb": "uzb", "aze": "aze",
	"geo": "geo", "arm": "arm", "alb": "alb", "baq": "baq",
}

// NormalizeLang maps a language tag to ISO 639-2/B, or "" for unknown.
func NormalizeLang(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "und" || s == "unknown" {
		return ""
	}
	if canonical, ok := langAliases[s]; ok {
		return canonical
	}
	return s
}

// filenameLanguages maps words that appear in release names to language codes.
var filenameLanguages = map[string]string{
	"english": "eng", "french": "fre", "francais": "fre", "truefrench": "fre",
	"german": "ger", "deutsch": "ger", "spanish": "spa", "espanol": "spa",
	"italian": "ita", "japanese": "jpn", "korean": "kor", "chinese": "chi",
	"mandarin": "chi", "cantonese": "chi", "russian": "rus",
	"portuguese": "por", "dutch": "dut", "swedish": "swe",
	"norwegian": "nor", "danish": "dan", "finnish": "fin", "polish": "pol",
	"czech": "cze", "hungarian": "hun", "arabic": "ara", "hebrew": "heb",
	"hindi": "hin", "thai": "tha", "turkish": "tur", "ukrainian": "ukr",
	"vietnamese": "vie", "greek": "gre", "romanian": "rum",
	"bulgarian": "bul", "croatian": "hrv", "serbian": "srp",
	"slovak": "slo", "slovenian": "slv", "estonian": "est",
	"latvian": "lav", "lithuanian": "lit", "icelandic": "ice",
	"indonesian": "ind", "malay": "may", "tagalog": "tgl",
	"persian": "per", "farsi": "per", "catalan": "cat",
	// three-letter release tags
	"eng": "eng", "fre": "fre", "ger": "ger", "spa": "spa", "ita": "ita",
	"jpn": "jpn", "kor": "kor", "chi": "chi", "rus": "rus", "por": "por",
	"dut": "dut", "swe": "swe", "nor": "nor", "dan": "dan", "fin": "fin",
	"pol": "pol", "cze": "cze", "hun": "hun", "ara": "ara", "heb": "heb",
	"hin": "hin", "tha": "tha", "tur": "tur", "ukr": "ukr", "vie": "vie",
	"gre": "gre",
}

// OriginalLanguageFromFilename guesses the original audio language from a
// release name. It returns "" when there is no signal or when several
// languages are named, because in that case guessing would be worse than
// falling back to the file's default track.
func OriginalLanguageFromFilename(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		path = path[i+1:]
	}
	tokens := strings.FieldsFunc(strings.ToLower(path), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})

	found := map[string]bool{}
	for _, t := range tokens {
		if lang, ok := filenameLanguages[t]; ok {
			found[lang] = true
		}
	}
	if len(found) == 1 {
		for lang := range found {
			return lang
		}
	}
	return ""
}
