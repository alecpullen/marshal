import { describe, expect, it } from 'vitest'
import {
  describePlannerEvent,
  plannerHandoffRequest,
  plannerRequests,
  questionAnswers,
  type PlannerEvent,
  type PlannerView,
} from './planner'

const ROOT = '/home/u/alpha'

const event = (sequence: number, kind: string, data: unknown): PlannerEvent => ({ sequence, kind, data })

const view = (requests: PlannerView['requests']): PlannerView =>
  ({ id: 'a'.repeat(32), projectRoot: ROOT, repoId: 'repo', requests }) as PlannerView

// One answer per question, in order: that is the only shape
// `session/request_question` settles with, and anything else is a decline.
describe('questionAnswers', () => {
  it('posts one answer per question, in the order the request asked them', () => {
    expect(questionAnswers(['one?', 'two?'], ['first', 'second'])).toEqual({
      answers: [
        { question: 'one?', answer: 'first' },
        { question: 'two?', answer: 'second' },
      ],
    })
  })

  it('keeps a question the person left blank rather than dropping it', () => {
    expect(questionAnswers(['one?', 'two?'], ['first'])).toEqual({
      answers: [
        { question: 'one?', answer: 'first' },
        { question: 'two?', answer: '' },
      ],
    })
  })
})

describe('plannerRequests', () => {
  it('carries every question a multi-question request asks', () => {
    const cards = plannerRequests(null, [
      event(1, 'turn.question', {
        requestId: 'req-1',
        method: 'session/request_question',
        params: { payload: { questions: [{ question: 'Which branch?' }, { question: 'Which runner?' }] } },
      }),
    ])
    expect(cards).toHaveLength(1)
    expect(cards[0].questions).toEqual(['Which branch?', 'Which runner?'])
  })

  it('does not re-ask a request the record reports as settled', () => {
    const cards = plannerRequests(
      view([{ requestId: 'req-1', method: 'session/request_question', state: 'answered', at: '2026-11-15T00:00:00Z' }]),
      [
        event(1, 'turn.question', {
          requestId: 'req-1',
          params: { payload: { questions: [{ question: 'Which branch?' }] } },
        }),
      ],
    )
    expect(cards).toEqual([])
  })
})

describe('describePlannerEvent', () => {
  it('reads a question from the update-shaped event the update path emits', () => {
    const line = describePlannerEvent(
      event(1, 'turn.question', { kind: 'question', update: { questions: [{ question: 'Which branch?' }] } }),
    )
    expect(line.text).toBe('Which branch?')
  })

  it('never offers the raw preview of a truncated request as the answer text', () => {
    const line = describePlannerEvent(
      event(1, 'turn.question', {
        requestId: 'req-2',
        params: { truncated: true, preview: '{"questions":[{"question":"a very long…' },
      }),
    )
    expect(line.text).toContain('exceeded the journal limit')
    expect(line.text).not.toContain('{"questions"')
  })
})

describe('plannerHandoffRequest', () => {
  const inputs = {
    requestId: 'req-1',
    title: 'a draft',
    plan: 'the plan',
    spec: 'the spec',
    repoId: 'repo',
    projectRoot: ROOT,
    noRequiredCiAcknowledged: true,
  }

  it('omits the target branch when neither the factory nor the repository resolved one', () => {
    expect(plannerHandoffRequest(inputs).config.targetBranch).toBeUndefined()
  })

  it('pins the branch the repository resolved', () => {
    expect(plannerHandoffRequest({ ...inputs, repoBranch: 'trunk' }).config.targetBranch).toBe('trunk')
  })
})
