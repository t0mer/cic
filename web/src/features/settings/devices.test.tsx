// @vitest-environment jsdom
import { QueryClient } from '@tanstack/react-query'
import { act, cleanup, fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { allDevicesKey, devicesKey } from '@/lib/api'
import { applyDeviceRemoved, applyDeviceUpdate } from '@/lib/device-cache'
import { makeDevice } from '@/lib/fixtures'
import { DeviceDeleteDialog } from './DeviceDeleteDialog'
import { DeviceEditDialog, devicePatch } from './DeviceEditDialog'
import { DevicesSection } from './DevicesSection'
import { renderWith, stubApi } from './test-utils'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const pixel = makeDevice({ full_name: 'Pixel', color: '', hidden: false })

describe('devicePatch', () => {
  it('is empty when nothing changed', () => {
    expect(devicePatch(pixel, 'Pixel', '', false)).toEqual({})
    expect(devicePatch(pixel, '  Pixel  ', '', false)).toEqual({})
  })
  it('includes only changed fields', () => {
    expect(devicePatch(pixel, 'Pixel 8', '', false)).toEqual({ full_name: 'Pixel 8' })
    expect(devicePatch(pixel, 'Pixel', '#ABCDEF', false)).toEqual({ color: '#abcdef' })
    expect(devicePatch(pixel, 'Pixel', '', true)).toEqual({ hidden: true })
  })
  it('whitespace runs are not a change: "a  b" against stored "a b"', () => {
    const ab = makeDevice({ full_name: 'a b' })
    expect(devicePatch(ab, 'a  b', '', false)).toEqual({})
    expect(devicePatch(ab, ' a   b ', '', false)).toEqual({})
    expect(devicePatch(ab, 'a  c', '', false)).toEqual({ full_name: 'a c' })
  })
  it('an emptied name resets to the device segment', () => {
    expect(devicePatch(pixel, '   ', '', false)).toEqual({ full_name: '' })
    const already = makeDevice({ full_name: 'pixel', device: 'pixel' })
    expect(devicePatch(already, '', '', false)).toEqual({})
  })
})

describe('DeviceEditDialog', () => {
  const open = (calls = stubApi(() => ({ json: { ...pixel, full_name: 'Pixel 8' } }))) => {
    const view = renderWith(
      <DeviceEditDialog device={pixel} open onOpenChange={() => {}} />,
      'admin',
    )
    return { calls, ...view }
  }

  it('Save is disabled until something changes, then sends only that field', async () => {
    const { calls, qc } = open()
    qc.setQueryData(devicesKey, [pixel])
    qc.setQueryData(allDevicesKey, [pixel])
    const save = screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement
    expect(save.disabled).toBe(true)
    fireEvent.change(screen.getByLabelText('Full name'), { target: { value: 'Pixel 8' } })
    expect(save.disabled).toBe(false)
    fireEvent.click(save)
    await waitFor(() => expect(calls).toHaveLength(1))
    expect(calls[0]).toMatchObject({
      method: 'PATCH',
      url: '/api/devices/dradis/pixel',
      body: { full_name: 'Pixel 8' },
    })
    await waitFor(() =>
      expect(qc.getQueryData<{ full_name: string }[]>(devicesKey)?.[0].full_name).toBe('Pixel 8'),
    )
    expect(qc.getQueryData<{ full_name: string }[]>(allDevicesKey)?.[0].full_name).toBe('Pixel 8')
  })

  it('clearing the name sends an empty string (reset to default)', async () => {
    const { calls } = open()
    fireEvent.change(screen.getByLabelText('Full name'), { target: { value: '' } })
    expect(screen.getByText(/Leave empty to use pixel/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(calls).toHaveLength(1))
    expect(calls[0].body).toEqual({ full_name: '' })
  })

  it('validates the hex field and previews the first grapheme', () => {
    open()
    const hex = screen.getByLabelText('Hex colour')
    fireEvent.change(hex, { target: { value: '#12' } })
    expect(screen.getByRole('alert').textContent).toMatch(/#1a73e8/)
    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(screen.getByLabelText('Full name'), { target: { value: 'שלום' } })
    expect(screen.getByTestId('device-avatar').textContent).toBe('ש')
    fireEvent.change(hex, { target: { value: '#112233' } })
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.getByTestId('device-avatar').getAttribute('style')).toMatch(/17, 34, 51/)
  })

  it('a swatch sets the colour and Automatic clears it; the switch toggles hidden', async () => {
    const { calls } = open()
    fireEvent.click(screen.getByRole('button', { name: 'Blue #1d4ed8' }))
    expect((screen.getByLabelText('Hex colour') as HTMLInputElement).value).toBe('#1d4ed8')
    fireEvent.click(screen.getByRole('button', { name: 'Automatic' }))
    expect((screen.getByLabelText('Hex colour') as HTMLInputElement).value).toBe('')
    fireEvent.click(screen.getByRole('switch', { name: 'Hidden' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(calls).toHaveLength(1))
    expect(calls[0].body).toEqual({ hidden: true })
  })

  it('shows the server error inline and keeps the dialog open', async () => {
    stubApi(() => ({ status: 404, json: { error: 'no such device', code: 'unknown_device' } }))
    renderWith(<DeviceEditDialog device={pixel} open onOpenChange={() => {}} />)
    fireEvent.change(screen.getByLabelText('Full name'), { target: { value: 'x' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByText('Unknown device')
  })

  it('guards against a double submit', async () => {
    let release: () => void = () => {}
    const gate = new Promise<void>((r) => (release = r))
    const calls = stubApi(async () => {
      await gate
      return { json: pixel }
    })
    renderWith(<DeviceEditDialog device={pixel} open onOpenChange={() => {}} />)
    fireEvent.change(screen.getByLabelText('Full name'), { target: { value: 'x' } })
    const form = screen.getByRole('button', { name: 'Save' }).closest('form') as HTMLFormElement
    fireEvent.submit(form)
    fireEvent.submit(form)
    await act(async () => release())
    expect(calls).toHaveLength(1)
  })
})

describe('DeviceDeleteDialog', () => {
  it('cannot be dismissed while the request is pending, then closes on success', async () => {
    let release: () => void = () => {}
    const gate = new Promise<void>((r) => (release = r))
    const calls = stubApi(async () => {
      await gate
      return { status: 204 }
    })
    const onOpenChange = vi.fn()
    renderWith(<DeviceDeleteDialog device={pixel} open onOpenChange={onOpenChange} />)
    expect(screen.getByText(/reappear if the phone publishes again/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    await screen.findByRole('button', { name: 'Deleting…' })
    const cancel = screen.getByRole('button', { name: 'Cancel' }) as HTMLButtonElement
    expect(cancel.disabled).toBe(true)
    fireEvent.keyDown(screen.getByTestId('confirm-dialog'), { key: 'Escape' })
    fireEvent.click(screen.getByRole('button', { name: 'Deleting…' }))
    expect(onOpenChange).not.toHaveBeenCalled()
    expect(calls).toHaveLength(1)
    await act(async () => release())
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
    expect(calls[0]).toMatchObject({ method: 'DELETE', url: '/api/devices/dradis/pixel' })
  })

  it('Esc closes it when nothing is pending', () => {
    stubApi(() => ({ status: 204 }))
    const onOpenChange = vi.fn()
    renderWith(<DeviceDeleteDialog device={pixel} open onOpenChange={onOpenChange} />)
    fireEvent.keyDown(screen.getByTestId('confirm-dialog'), { key: 'Escape' })
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })
})

describe('Devices section and SSE-style cache patches', () => {
  const hidden = makeDevice({
    key: 'k/h',
    prefix: 'k',
    device: 'h',
    full_name: 'Ghost',
    hidden: true,
  })
  const other = makeDevice({ key: 'k/o', prefix: 'k', device: 'o', full_name: 'Other' })

  it('lists hidden devices with a badge and requests include_hidden', async () => {
    const calls = stubApi(() => ({ json: [pixel, hidden] }))
    renderWith(<DevicesSection />)
    await screen.findByText('Ghost')
    expect(calls[0].url).toBe('/api/devices?include_hidden=1')
    const rows = screen.getAllByTestId('settings-device-row')
    expect(rows).toHaveLength(2)
    expect(rows[0].textContent).toContain('Ghost')
    expect(rows[0].textContent).toContain('Hidden')
    expect(screen.getByRole('button', { name: 'Edit Pixel' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Delete Ghost' })).toBeTruthy()
  })

  it('device.removed drops the row from both lists', async () => {
    stubApi(() => ({ json: [pixel, other] }))
    const qc = new QueryClient()
    qc.setQueryData(devicesKey, [pixel, other])
    renderWith(<DevicesSection />, 'admin', qc)
    await screen.findByText('Other')
    act(() => applyDeviceRemoved(qc, 'k/o'))
    await waitFor(() => expect(screen.queryByText('Other')).toBeNull())
    expect(qc.getQueryData<unknown[]>(devicesKey)).toHaveLength(1)
  })

  it('device.updated hiding keeps the row in Settings but leaves the Cards list', async () => {
    stubApi(() => ({ json: [pixel, other] }))
    const qc = new QueryClient()
    qc.setQueryData(devicesKey, [pixel, other])
    renderWith(<DevicesSection />, 'admin', qc)
    await screen.findByText('Other')
    act(() => applyDeviceUpdate(qc, { ...other, hidden: true }))
    await waitFor(() => expect(screen.getAllByText('Hidden').length).toBe(1))
    expect(screen.getByText('Other')).toBeTruthy()
    expect(qc.getQueryData<{ key: string }[]>(devicesKey)?.map((d) => d.key)).toEqual([pixel.key])
  })
})

describe('device cache rules', () => {
  it('an update never seeds a list that was not loaded (Cards stays empty)', () => {
    const qc = new QueryClient()
    qc.setQueryData(allDevicesKey, [pixel])
    applyDeviceUpdate(qc, { ...pixel, full_name: 'New' })
    expect(qc.getQueryData(devicesKey)).toBeUndefined()
    expect(qc.getQueryData<{ full_name: string }[]>(allDevicesKey)?.[0].full_name).toBe('New')
  })
  it('SSE device.updated before useDevices loaded does not seed it', () => {
    const qc = new QueryClient()
    applyDeviceUpdate(qc, pixel)
    expect(qc.getQueryData(devicesKey)).toBeUndefined()
    expect(qc.getQueryData(allDevicesKey)).toBeUndefined()
  })
  it('a removal does not create a list either, and the absent list is invalidated', () => {
    const qc = new QueryClient()
    qc.setQueryData(devicesKey, [pixel])
    const spy = vi.spyOn(qc, 'invalidateQueries')
    applyDeviceRemoved(qc, pixel.key)
    expect(qc.getQueryData(devicesKey)).toEqual([])
    expect(qc.getQueryData(allDevicesKey)).toBeUndefined()
    expect(spy).toHaveBeenCalledWith({ queryKey: allDevicesKey, exact: true })
  })
  it('editing in Settings with Cards never loaded does not seed Cards', async () => {
    stubApi(() => ({ json: { ...pixel, full_name: 'Pixel 8' } }))
    const qc = new QueryClient()
    qc.setQueryData(allDevicesKey, [pixel])
    renderWith(<DeviceEditDialog device={pixel} open onOpenChange={() => {}} />, 'admin', qc)
    fireEvent.change(screen.getByLabelText('Full name'), { target: { value: 'Pixel 8' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(qc.getQueryData<{ full_name: string }[]>(allDevicesKey)?.[0].full_name).toBe(
        'Pixel 8',
      ),
    )
    expect(qc.getQueryData(devicesKey)).toBeUndefined()
  })
  it('a name with a zero-width character blocks Save with an inline message', () => {
    stubApi(() => ({ json: pixel }))
    renderWith(<DeviceEditDialog device={pixel} open onOpenChange={() => {}} />)
    fireEvent.change(screen.getByLabelText('Full name'), { target: { value: 'a\u200Bb' } })
    expect(screen.getByText(/cannot contain control, invisible/)).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('a refetch in flight when the save lands cannot resurrect old data', async () => {
    let releaseList: () => void = () => {}
    const listGate = new Promise<void>((r) => (releaseList = r))
    const qc = new QueryClient()
    qc.setQueryData(allDevicesKey, [pixel])
    qc.setQueryData(devicesKey, [pixel])
    stubApi(async (c) => {
      if (c.method === 'PATCH') return { json: { ...pixel, full_name: 'Pixel 8' } }
      await listGate
      return { json: [pixel] } // stale snapshot taken before the edit
    })
    const refetch = qc.fetchQuery({
      queryKey: allDevicesKey,
      queryFn: () => fetch('/api/devices?include_hidden=1').then((r) => r.json()),
      staleTime: 0,
    })
    renderWith(<DeviceEditDialog device={pixel} open onOpenChange={() => {}} />, 'admin', qc)
    fireEvent.change(screen.getByLabelText('Full name'), { target: { value: 'Pixel 8' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(qc.getQueryData<{ full_name: string }[]>(allDevicesKey)?.[0].full_name).toBe(
        'Pixel 8',
      ),
    )
    await act(async () => {
      releaseList()
      await refetch.catch(() => undefined)
    })
    expect(qc.getQueryData<{ full_name: string }[]>(allDevicesKey)?.[0].full_name).toBe('Pixel 8')
  })

  it('unknown_device on edit drops the device and invalidates both lists', async () => {
    stubApi(() => ({ status: 404, json: { error: 'no such device', code: 'unknown_device' } }))
    const qc = new QueryClient()
    qc.setQueryData(allDevicesKey, [pixel])
    qc.setQueryData(devicesKey, [pixel])
    const spy = vi.spyOn(qc, 'invalidateQueries')
    renderWith(<DeviceEditDialog device={pixel} open onOpenChange={() => {}} />, 'admin', qc)
    fireEvent.change(screen.getByLabelText('Full name'), { target: { value: 'x' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByText('Unknown device')
    expect(qc.getQueryData(allDevicesKey)).toEqual([])
    expect(qc.getQueryData(devicesKey)).toEqual([])
    expect(spy).toHaveBeenCalledWith({ queryKey: devicesKey })
  })

  it('not_found on delete drops the device and shows the friendly text', async () => {
    stubApi(() => ({ status: 404, json: { error: 'gone', code: 'not_found' } }))
    const qc = new QueryClient()
    qc.setQueryData(allDevicesKey, [pixel])
    qc.setQueryData(devicesKey, [pixel])
    renderWith(<DeviceDeleteDialog device={pixel} open onOpenChange={() => {}} />, 'admin', qc)
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    await screen.findByText('That item no longer exists')
    expect(qc.getQueryData(allDevicesKey)).toEqual([])
    expect(qc.getQueryData(devicesKey)).toEqual([])
  })
})
