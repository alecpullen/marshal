import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import Notifications from './Notifications.svelte'
import * as api from '../../lib/api'

vi.mock('../../lib/api', async (importActual) => {
  const actual = await importActual<typeof import('../../lib/api')>()
  return { ...actual, getNotifications: vi.fn(), saveNotifications: vi.fn(), testNotifications: vi.fn(), listSecrets: vi.fn() }
})

beforeEach(() => {
  localStorage.clear()
  ;(api.getNotifications as Mock).mockResolvedValue({ webhooks: [] })
  ;(api.listSecrets as Mock).mockResolvedValue(['vault:hooks/signing'])
  ;(api.saveNotifications as Mock).mockImplementation(async (s) => ({ webhooks: s.webhooks.map((w: api.Webhook, i: number) => ({ id: `w${i}`, ...w })) }))
  vi.stubGlobal('Notification', Object.assign(vi.fn(), { permission: 'default', requestPermission: vi.fn().mockResolvedValue('granted') }))
})
afterEach(() => {
  cleanup()
  vi.clearAllMocks()
  vi.unstubAllGlobals()
})

describe('Notifications settings', () => {
  it('requests permission and shows the result', async () => {
    render(Notifications, { onToast: vi.fn() })
    await fireEvent.click(screen.getByRole('button', { name: 'Request permission' }))
    await waitFor(() => expect(screen.getByTestId('permission').textContent).toContain('Allowed'))
    expect(Notification.requestPermission).toHaveBeenCalled()
  })

  it('per-event toggles persist in the browser', async () => {
    render(Notifications, { onToast: vi.fn() })
    await fireEvent.click(screen.getByLabelText('Notify: A budget cap was reached'))
    expect(JSON.parse(localStorage.getItem('marshal.ui.notify')!).budget).toBe(false)
  })

  it('the webhook form posts the URL, secret ref and events', async () => {
    render(Notifications, { onToast: vi.fn() })
    await waitFor(() => expect(api.listSecrets).toHaveBeenCalled())
    await fireEvent.input(screen.getByLabelText('URL'), { target: { value: 'https://hooks.example.com/x' } })
    await fireEvent.change(await screen.findByLabelText('Signing secret'), { target: { value: 'vault:hooks/signing' } })
    await fireEvent.click(screen.getByLabelText('Webhook event: A watch fired'))
    await fireEvent.click(screen.getByRole('button', { name: 'Add webhook' }))
    await waitFor(() => expect(api.saveNotifications).toHaveBeenCalled())
    const sent = (api.saveNotifications as Mock).mock.calls[0][0].webhooks[0]
    expect(sent).toMatchObject({ url: 'https://hooks.example.com/x', secretRef: 'vault:hooks/signing' })
    expect(sent.events).not.toContain('watch_fired')
    expect(sent.events).toContain('budget')
    expect(await screen.findByTestId('webhook')).toBeTruthy()
  })

  it('rejects a non-http URL without calling the bridge', async () => {
    render(Notifications, { onToast: vi.fn() })
    await fireEvent.input(screen.getByLabelText('URL'), { target: { value: 'ftp://x' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Add webhook' }))
    expect((await screen.findByRole('alert')).textContent).toContain('http')
    expect(api.saveNotifications).not.toHaveBeenCalled()
  })

  it('Test sends to the configured webhooks', async () => {
    ;(api.getNotifications as Mock).mockResolvedValue({ webhooks: [{ id: 'w1', url: 'https://h', events: ['budget'] }] })
    ;(api.testNotifications as Mock).mockResolvedValue({ sent: 1 })
    const onToast = vi.fn()
    render(Notifications, { onToast })
    await screen.findByTestId('webhook')
    await fireEvent.click(screen.getByRole('button', { name: 'Test' }))
    await waitFor(() => expect(onToast).toHaveBeenCalledWith('Sent a test to 1 webhook'))
  })
})
