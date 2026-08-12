import { describe, expect, it } from 'vitest'
import { cn } from './utils'

describe('cn', () => {
	it('joins and merges classes', () => {
		expect(cn('a', 'b')).toBe('a b')
		expect(cn('p-2', 'p-4')).toBe('p-4')
	})
})