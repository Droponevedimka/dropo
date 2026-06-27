package main

// LocalDomainSuffixes are always resolved and routed locally.
var LocalDomainSuffixes = []string{
	"local", "internal", "corp", "lan", "home", "intranet", "private",
}

// RuDomainSuffixes are Russian and Russia-facing domain suffixes routed direct
// by default. Values intentionally do not start with a dot: sing-box
// domain_suffix rules expect suffix tokens such as "ozon.ru", not ".ozon.ru".
var RuDomainSuffixes = []string{
	// Top-level domains
	"ru", "su", "xn--p1ai",

	// Yandex
	"yandex.com", "yandex.net", "yandex.ru", "ya.ru", "yandex.by", "yandex.kz",

	// VK / Mail.ru
	"vk.com", "vkontakte.ru", "vk.me", "userapi.com",
	"vk.ru", "vkvideo.ru", "vkvideo.com", "vkuseraudio.net",
	"mail.ru", "mailru.com", "mycdn.me", "imgsmail.ru",
	"ok.ru", "odnoklassniki.ru",

	// Banks
	"sberbank.ru", "sber.ru", "tinkoff.ru", "tinkoff.com", "vtb.ru", "alfabank.ru",
	"raiffeisen.ru", "gazprombank.ru", "open.ru", "rosbank.ru",

	// Government
	"gosuslugi.ru", "mos.ru", "nalog.ru", "government.ru", "kremlin.ru",
	"duma.gov.ru", "cbr.ru", "pfrf.ru", "fss.ru",

	// News
	"ria.ru", "rbc.ru", "interfax.ru", "tass.ru", "kommersant.ru",
	"lenta.ru", "gazeta.ru", "kp.ru", "mk.ru", "iz.ru", "rt.com",

	// E-commerce
	"ozon.ru", "wildberries.ru", "lamoda.ru", "dns-shop.ru", "mvideo.ru",
	"eldorado.ru", "citilink.ru", "avito.ru", "youla.ru",

	// Retail
	"perekrestok.ru", "magnit.ru", "5ka.ru", "dixy.ru", "lenta.com",
	"sbermarket.ru", "delivery-club.ru",

	// Transport and delivery
	"rzd.ru", "aeroflot.ru", "s7.ru", "utair.ru", "pobeda.aero",
	"pochta.ru", "cdek.ru", "boxberry.ru", "dpd.ru",

	// Telecom
	"mts.ru", "megafon.ru", "beeline.ru", "tele2.ru",
	"rostelecom.ru", "rt.ru",

	// Media
	"vgtrk.ru", "1tv.ru", "ntv.ru", "ren.tv", "ctc.ru",
	"rutube.ru", "ivi.ru", "okko.tv", "more.tv", "kinopoisk.ru",
	"dzen.ru", "zen.yandex.ru",

	// Maps / navigation
	"2gis.ru", "2gis.com",

	// Education and development
	"javascript.ru", "learn.javascript.ru", "stepik.org", "netology.ru", "geekbrains.ru",

	// Other popular services
	"sports.ru", "championat.com", "sport-express.ru",
	"hh.ru", "superjob.ru", "rabota.ru",
	"cian.ru", "domclick.ru",
	"pikabu.ru", "habr.com", "vc.ru", "dtf.ru",
}

// RuDomainKeywords are additional keyword matches for Russian domains.
var RuDomainKeywords = []string{
	"yandex", "sber", "tinkoff", "gosuslugi", "rutube",
	"vkontakte", "mailru", "rambler", "wildberries", "ozon",
}

// DirectIPCIDRs are non-blocked service IP ranges that are commonly used
// without a hostname visible to route rules. Keep this list conservative.
var DirectIPCIDRs = []string{}

// DirectProcessNames are native apps that should stay outside VPN/free-access
// in blocked_only mode unless the user explicitly selects an all-traffic mode.
var DirectProcessNames = []string{}
