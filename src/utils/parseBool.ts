// Mirrors Go strconv.ParseBool, which settings.Service.Bool uses; anything it rejects is false.
const TRUE_LITERALS = new Set(['1', 't', 'T', 'TRUE', 'true', 'True'])

export function parseBool(raw: string | undefined): boolean {
  return raw !== undefined && TRUE_LITERALS.has(raw)
}
