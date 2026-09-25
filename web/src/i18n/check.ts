// Build-time proof that the panel has words for everything the device can say.
//
// The device sends `labelKey` with every staged change (#27), from a fixed set
// generated into diffLabelKeys.ts by a Go test that fails when the file is
// stale. Here that set is checked against the English bundle's `diff` tree:
// a key with no translation is a type error, so `vue-tsc` — and with it the
// release build — refuses to ship an untranslated row. Every other locale must
// satisfy the English shape already, so English covering the set means all do.
import { DIFF_LABEL_KEYS } from './diffLabelKeys'
import type { MessageSchema } from './messages/en'

/** Every dotted path to a string inside T: `network.uplink.proto`, … */
type LeafPaths<T> = {
  [K in keyof T & string]: T[K] extends string ? K : `${K}.${LeafPaths<T[K]>}`
}[keyof T & string]

export type DiffLabelKey = LeafPaths<MessageSchema['diff']>

export const TRANSLATED_DIFF_KEYS: readonly DiffLabelKey[] = DIFF_LABEL_KEYS
