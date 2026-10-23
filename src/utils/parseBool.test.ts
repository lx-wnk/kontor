import { describe, expect, it } from 'vitest'
import { parseBool } from './parseBool'

describe('parseBool', () => {
  it.each(['1', 't', 'T', 'TRUE', 'true', 'True'])('reads %s as true like Go strconv.ParseBool', (raw) => {
    expect(parseBool(raw)).toBe(true)
  })

  it.each(['0', 'f', 'F', 'FALSE', 'false', 'False'])('reads %s as false', (raw) => {
    expect(parseBool(raw)).toBe(false)
  })

  it.each(['', 'yes', 'on', 'tRuE', ' true', 'true ', '2'])('reads the unparseable %j as false', (raw) => {
    expect(parseBool(raw)).toBe(false)
  })

  it('reads undefined as false', () => {
    expect(parseBool(undefined)).toBe(false)
  })
})
