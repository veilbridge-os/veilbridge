// Wi-Fi words and helpers shared by the Wi-Fi screen and the apply bar (#57).

type T = (key: string, named?: Record<string, unknown>) => string

/** "2.4" → "2,4" in Russian: the band as the locale writes a decimal. */
export function bandWord(band: string, locale = 'en'): string {
  const n = Number(band)
  return Number.isFinite(n) ? n.toLocaleString(locale) : band
}

/** Security as printed on the box (D-104). */
export function securityWord(t: T, security: string): string {
  switch (security) {
    case 'wpa2':
      return 'WPA2'
    case 'wpa2-wpa3':
      return 'WPA2/WPA3'
    case 'wpa3':
      return 'WPA3'
    case 'open':
      return t('wifi.secOpen')
  }
  return t('wifi.secOther')
}

/** A country code by its name in the reader's language. */
export function countryName(code: string, locale: string): string {
  if (!code) return ''
  try {
    return new Intl.DisplayNames([locale], { type: 'region' }).of(code) ?? code
  } catch {
    return code
  }
}

// ISO 3166-1 alpha-2. The router knows its own list (iwinfo countrylist); this
// one only feeds the picker, and the router refuses a code it does not know.
export const COUNTRIES =
  'AD AE AF AG AI AL AM AO AR AS AT AU AW AZ BA BB BD BE BF BG BH BI BJ BM BN BO BR BS BT BW BY BZ CA CD CF CG CH CI CL CM CN CO CR CU CV CY CZ DE DJ DK DM DO DZ EC EE EG ER ES ET FI FJ FM FR GA GB GD GE GF GH GL GM GN GP GR GT GU GW GY HK HN HR HT HU ID IE IL IN IQ IR IS IT JM JO JP KE KG KH KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MT MU MV MW MX MY MZ NA NE NG NI NL NO NP NZ OM PA PE PF PG PH PK PL PM PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SI SK SL SM SN SO SR SS ST SV SY SZ TC TD TG TH TJ TL TM TN TO TR TT TW TZ UA UG US UY UZ VA VC VE VI VN VU WF WS YE YT ZA ZM ZW'.split(
    ' ',
  )

/** The name counts in BYTES: a radio broadcasts at most 32, and a Cyrillic
 * letter takes two of them. */
export function ssidBytes(s: string): number {
  return new TextEncoder().encode(s).length
}

/** Only printable ASCII fits a WPA password (8–63). */
export function passwordProblem(p: string): 'short' | 'long' | 'chars' | '' {
  if (p.length === 64 && /^[0-9a-fA-F]+$/.test(p)) return ''
  if (p.length < 8) return 'short'
  if (p.length > 63) return 'long'
  if (!/^[\x20-\x7e]+$/.test(p)) return 'chars'
  return ''
}

/** A password people can read off a sheet and type on a TV remote: 12 letters
 * and digits without the look-alikes (0/O, 1/l/I), in groups of four — about
 * 60 bits, enough against an offline guess at a captured handshake. */
export function generatePassword(): string {
  const alphabet = 'abcdefghjkmnpqrstuvwxyz23456789'
  const bytes = new Uint8Array(12)
  crypto.getRandomValues(bytes)
  let out = ''
  bytes.forEach((b, i) => {
    if (i && i % 4 === 0) out += '-'
    out += alphabet[b % alphabet.length]
  })
  return out
}

/** The text a phone camera reads to join a network. Special characters are
 * escaped as the format asks (\\ ; , : "). */
export function wifiQRText(ssid: string, password: string, security: string): string {
  const esc = (s: string) => s.replace(/([\\;,:"])/g, '\\$1')
  const kind = security === 'open' ? 'nopass' : security === 'wpa3' ? 'SAE' : 'WPA'
  return `WIFI:T:${kind};S:${esc(ssid)};${kind === 'nopass' ? '' : `P:${esc(password)};`};`
}

/** A QR code as the size of its grid and one SVG path of its dark modules,
 * made in the browser: the password never leaves the page. The page draws it
 * itself — no markup from a library is injected. The generator is loaded only
 * when a code is shown. */
export interface QRPicture {
  size: number
  path: string
}
export async function wifiQR(text: string): Promise<QRPicture> {
  const { default: qrcode } = await import('qrcode-generator')
  // The library's default takes the low byte of each character, which turns
  // a Cyrillic network name into noise. UTF-8 is what phones expect.
  qrcode.stringToBytes = (s: string) => Array.from(new TextEncoder().encode(s))
  const qr = qrcode(0, 'M')
  qr.addData(text, 'Byte')
  qr.make()
  const n = qr.getModuleCount()
  let path = ''
  for (let y = 0; y < n; y++)
    for (let x = 0; x < n; x++) if (qr.isDark(y, x)) path += `M${x} ${y}h1v1h-1z`
  return { size: n, path }
}
