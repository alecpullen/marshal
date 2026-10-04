<script lang="ts">
  import { onMount, untrack } from 'svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Modal from '../../lib/ui/Modal.svelte'
  import Segmented from '../../lib/ui/Segmented.svelte'
  import DiffLines from '../../lib/DiffLines.svelte'
  import SourceEditor from '../../lib/workspaces/SourceEditor.svelte'
  import SidePanel from '../../lib/workspaces/SidePanel.svelte'
  import BaseLayer from '../../lib/workspaces/layers/BaseLayer.svelte'
  import ToolchainsLayer from '../../lib/workspaces/layers/ToolchainsLayer.svelte'
  import PackagesLayer from '../../lib/workspaces/layers/PackagesLayer.svelte'
  import MountsLayer from '../../lib/workspaces/layers/MountsLayer.svelte'
  import FilesLayer from '../../lib/workspaces/layers/FilesLayer.svelte'
  import SecretsLayer from '../../lib/workspaces/layers/SecretsLayer.svelte'
  import NetworkLayer from '../../lib/workspaces/layers/NetworkLayer.svelte'
  import ResourcesLayer from '../../lib/workspaces/layers/ResourcesLayer.svelte'
  import SetupLayer from '../../lib/workspaces/layers/SetupLayer.svelte'
  import {
    diffWorkspace, errMessage, getNetworkHosts, getSecretsStatus, getWorkspace, listBuilds, listRepos, patchWorkspace,
    publishWorkspace, rotateWorkspaceCA, saveWorkspaceDraft, setWorkspacePool, startBuild,
    type BuildsInfo, type NetRow, type RepoInfo, type SecretsStatus, type WSNetwork,
  } from '../../lib/api'
  import { applyParse, applyServer, cursorLine, diagsInLayer, editSource, initialState, selectLayer, type DesignerState } from '../../lib/workspaces/designer'
  import { changedLines } from '../../lib/workspaces/sourceEditor'

  let { name, onNavigate }: { name: string; onNavigate: (hash: string) => void } = $props()

  type View = 'layers' | 'both' | 'source'
  const VIEW_KEY = 'marshal.ui.ws.view'
  const VIEWS: readonly string[] = ['layers', 'both', 'source']

  let view = $state<View>('both')
  let st = $state<DesignerState>(initialState)
  let loadError = $state('')
  let error = $state('')
  let saving = $state<'saved' | 'saving' | 'unsaved'>('saved')
  let flash = $state<number[]>([])
  let builds = $state<BuildsInfo | null>(null)
  let secrets = $state<SecretsStatus | null>(null)
  let repos = $state<RepoInfo[]>([])
  let hosts = $state<NetRow[]>([])
  let publishedNet = $state<WSNetwork | null>(null)
  let publishDiff = $state<{ text: string; first: boolean } | null>(null)
  let busy = $state(false)
  let flashTimer: ReturnType<typeof setTimeout> | undefined
  // Source responses older than the latest edit are dropped.
  let editSeq = 0

  const published = $derived(builds?.versions?.length ? builds.versions[builds.versions.length - 1].n : 0)

  async function loadAux() {
    // Each of these is decoration: a bridge without the route leaves its panel empty rather than failing the page.
    const [b, s, r, h] = await Promise.allSettled([listBuilds(name), getSecretsStatus(), listRepos(), getNetworkHosts(name)])
    if (b.status === 'fulfilled') builds = b.value
    if (s.status === 'fulfilled') secrets = s.value
    if (r.status === 'fulfilled') repos = r.value
    if (h.status === 'fulfilled') hosts = h.value.rows
    const n = builds?.versions?.length ? builds.versions[builds.versions.length - 1].n : 0
    if (n) {
      try {
        publishedNet = (await getWorkspace(name, n)).doc.network
      } catch {
        publishedNet = null
      }
    }
  }

  onMount(() => {
    try {
      const v = localStorage.getItem(VIEW_KEY)
      if (v && VIEWS.includes(v)) view = v as View
    } catch {
      // A blocked store only loses the remembered view.
    }
    void (async () => {
      try {
        st = applyServer(initialState, await getWorkspace(name))
      } catch (e) {
        loadError = errMessage(e)
        return
      }
      void loadAux()
    })()
    return () => clearTimeout(flashTimer)
  })

  function setView(v: string) {
    view = v as View
    try {
      localStorage.setItem(VIEW_KEY, v)
    } catch {
      // See above.
    }
  }

  const select = (n: number) => (st = selectLayer(st, n))

  async function patch(layer: number, value: unknown) {
    error = ''
    saving = 'saving'
    const before = untrack(() => st.source)
    try {
      const r = await patchWorkspace(name, layer, value)
      st = applyServer(st, r)
      // Layer 0 is the policy, which has no card to select.
      if (layer > 0) st = selectLayer(st, layer)
      flash = changedLines(before, r.source)
      clearTimeout(flashTimer)
      flashTimer = setTimeout(() => (flash = []), 1200)
      saving = 'saved'
    } catch (e) {
      error = errMessage(e)
      saving = 'unsaved'
    }
  }

  async function sourceChanged(text: string) {
    const seq = ++editSeq
    st = editSource(st, text)
    saving = 'saving'
    try {
      const r = await saveWorkspaceDraft(name, text)
      if (seq !== editSeq) return
      st = applyParse(st, r)
      saving = 'saved'
      error = ''
    } catch (e) {
      if (seq !== editSeq) return
      error = errMessage(e)
      saving = 'unsaved'
    }
  }

  const cursor = (line: number) => (st = cursorLine(st, line))

  async function askPublish() {
    error = ''
    try {
      if (published === 0) {
        publishDiff = { text: '', first: true }
        return
      }
      publishDiff = { text: await diffWorkspace(name, published, 0), first: false }
    } catch (e) {
      error = errMessage(e)
    }
  }

  async function publish() {
    busy = true
    try {
      await publishWorkspace(name)
      publishDiff = null
      await loadAux()
    } catch (e) {
      error = errMessage(e)
      publishDiff = null
    } finally {
      busy = false
    }
  }

  async function build() {
    busy = true
    error = ''
    try {
      await startBuild(name, published || undefined)
      onNavigate(`#workspaces/${encodeURIComponent(name)}/builds`)
    } catch (e) {
      error = errMessage(e)
    } finally {
      busy = false
    }
  }

  async function pool(size: number) {
    try {
      await setWorkspacePool(name, size)
      if (builds) builds = { ...builds, pool: { idle: 0, starting: 0, ...builds.pool, size } }
    } catch (e) {
      error = errMessage(e)
    }
  }

  const VIEW_OPTIONS = [
    { value: 'layers', label: 'Layers' },
    { value: 'both', label: 'Layers + source' },
    { value: 'source', label: 'Source' },
  ]
  const cardProps = (layer: number) => ({ diags: diagsInLayer(st, layer), selected: st.selectedLayer === layer, onSelect: () => select(layer), onPatch: patch })
</script>

<div class="mx-auto flex max-w-7xl flex-col gap-4 p-6">
  <header class="flex flex-wrap items-center gap-3">
    <button type="button" class="cursor-pointer text-sm text-muted hover:text-fg" onclick={() => onNavigate('#workspaces')}>← Workspaces</button>
    <h1 class="text-lg font-semibold">{name}</h1>
    <span class="text-xs text-muted" data-testid="save-state">{saving === 'saved' ? 'saved' : saving === 'saving' ? 'saving…' : 'unsaved'}</span>
    <span class="flex-1"></span>
    <Segmented label="View" options={VIEW_OPTIONS} value={view} onchange={setView} />
    <Button variant="ghost" onclick={askPublish} disabled={busy || !st.doc}>Publish</Button>
    <Button onclick={build} disabled={busy || published === 0} title={published === 0 ? 'Publish first' : undefined}>Build</Button>
    <Button variant="ghost" disabled title="Arrives with the terminal in W5">Test shell (W5)</Button>
  </header>

  {#if loadError}<p role="alert" class="text-sm text-err">{loadError}</p>{/if}
  {#if error}<p role="alert" class="text-sm text-err" data-testid="designer-error">{error}</p>{/if}

  {#if st.doc}
    {@const doc = st.doc}
    <div class="grid gap-4 lg:grid-cols-[minmax(0,1fr)_16rem]">
      <div class="grid min-w-0 gap-4 {view === 'both' ? 'xl:grid-cols-2' : ''}">
        {#if view !== 'source'}
          <div class="flex flex-col gap-3" data-testid="layers">
            <BaseLayer {doc} {...cardProps(1)} />
            <ToolchainsLayer {doc} {...cardProps(2)} />
            <PackagesLayer {doc} {...cardProps(3)} />
            <MountsLayer {doc} {...cardProps(4)} {repos} />
            <FilesLayer {doc} {...cardProps(5)} />
            <SecretsLayer {doc} {...cardProps(6)} {secrets} />
            <NetworkLayer {doc} {...cardProps(7)} {hosts} published={publishedNet} />
            <ResourcesLayer {doc} {...cardProps(8)} />
            <SetupLayer {doc} {...cardProps(9)} />
          </div>
        {/if}
        {#if view !== 'layers'}
          <SourceEditor value={st.source} diagnostics={st.diagnostics} highlight={st.highlight} {flash} onChange={sourceChanged} onCursorLine={cursor} />
        {/if}
      </div>
      <SidePanel {name} {doc} {builds} onPatch={patch} onPool={pool} onRotateCA={() => rotateWorkspaceCA(name)} {onNavigate} />
    </div>
  {:else if !loadError}
    <p class="text-sm text-muted">Loading…</p>
  {/if}
</div>

{#if publishDiff}
  <Modal title="Publish {name}?" description={publishDiff.first ? 'This is the first version.' : `Changes since v${published}:`} onDismiss={() => void (publishDiff = null)}>
    {#if !publishDiff.first}
      {#if publishDiff.text.trim()}<DiffLines diff={publishDiff.text} />{:else}<p class="text-sm text-muted">No changes from the published version.</p>{/if}
    {/if}
    {#snippet footer()}
      <Button variant="ghost" onclick={() => (publishDiff = null)}>Cancel</Button>
      <Button onclick={publish} disabled={busy}>Publish</Button>
    {/snippet}
  </Modal>
{/if}
