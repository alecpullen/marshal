<script lang="ts">
  import { untrack } from 'svelte'
  import LayerCard from '../LayerCard.svelte'
  import type { SecretsStatus } from '../../api'
  import type { LayerProps } from './types'

  let { doc, diags, selected, onSelect, onPatch, secrets }: LayerProps & { secrets: SecretsStatus | null } = $props()

  interface Env { name: string; ref: string }
  interface Inj { host: string; ref: string; header: string; format: string }
  const envFrom = (m: Record<string, string>): Env[] => Object.entries(m).map(([name, ref]) => ({ name, ref }))
  const injFrom = (m: LayerProps['doc']['inject']): Inj[] => Object.entries(m).map(([host, v]) => ({ host, ref: v.ref, header: v.header, format: v.format }))
  const envOk = (r: Env) => r.name.trim() !== '' && r.ref.trim() !== ''
  const injOk = (r: Inj) => r.host.trim() !== '' && r.ref.trim() !== ''

  // svelte-ignore state_referenced_locally
  let env = $state<Env[]>(envFrom(doc.secretsEnv))
  // svelte-ignore state_referenced_locally
  let inj = $state<Inj[]>(injFrom(doc.inject))
  $effect(() => {
    const e = doc.secretsEnv
    const i = doc.inject
    untrack(() => {
      env = [...envFrom(e), ...env.filter((r) => !envOk(r))]
      inj = [...injFrom(i), ...inj.filter((r) => !injOk(r))]
    })
  })

  function commit() {
    const out = {
      secretsEnv: Object.fromEntries(env.filter(envOk).map((r) => [r.name, r.ref])),
      inject: Object.fromEntries(inj.filter(injOk).map((r) => [r.host, { ref: r.ref, header: r.header, format: r.format }])),
    }
    const cur = { secretsEnv: doc.secretsEnv, inject: Object.fromEntries(Object.entries(doc.inject).map(([k, v]) => [k, { ref: v.ref, header: v.header, format: v.format }])) }
    if (JSON.stringify(out) !== JSON.stringify(cur)) onPatch(6, out)
  }
  const input = 'rounded border border-border bg-bg px-1.5 py-1 font-mono text-xs'
</script>

<LayerCard layer={6} title="Secrets" {selected} {diags} {onSelect}>
  {#if secrets?.backend === 'env'}
    <p class="text-xs text-warn" data-testid="secrets-warning">Injection needs the local or OpenBao backend.</p>
  {/if}
  <p class="text-xs text-muted">Environment (last resort)</p>
  {#each env as r, i (i)}
    <div class="flex items-center gap-1" data-testid="env-row">
      <input class="w-32 {input}" placeholder="NAME" aria-label="Env name" bind:value={r.name} onchange={commit} />
      <input class="w-40 {input}" placeholder="vault:ref" aria-label="Env ref" bind:value={r.ref} onchange={commit} />
      <button type="button" class="cursor-pointer text-muted hover:text-err" aria-label="Remove env" onclick={() => ((env = env.filter((_, j) => j !== i)), commit())}>×</button>
    </div>
  {/each}
  <button type="button" class="w-fit cursor-pointer rounded border border-border px-2 py-1 text-xs hover:bg-hover" onclick={() => env.push({ name: '', ref: '' })}>Add env</button>

  <p class="mt-1 text-xs text-muted">Inject at the proxy</p>
  {#each inj as r, i (i)}
    <div class="flex flex-wrap items-center gap-1" data-testid="inject-row">
      <input class="w-36 {input}" placeholder="host" aria-label="Inject host" bind:value={r.host} onchange={commit} />
      <input class="w-36 {input}" placeholder="vault:ref" aria-label="Inject ref" bind:value={r.ref} onchange={commit} />
      <input class="w-28 {input}" placeholder="Authorization" aria-label="Inject header" bind:value={r.header} onchange={commit} />
      <input class="w-24 {input}" placeholder="Bearer {'{}'}" aria-label="Inject format" bind:value={r.format} onchange={commit} />
      <button type="button" class="cursor-pointer text-muted hover:text-err" aria-label="Remove inject" onclick={() => ((inj = inj.filter((_, j) => j !== i)), commit())}>×</button>
    </div>
  {/each}
  <button type="button" class="w-fit cursor-pointer rounded border border-border px-2 py-1 text-xs hover:bg-hover" onclick={() => inj.push({ host: '', ref: '', header: 'Authorization', format: 'Bearer {}' })}>Add injection</button>
</LayerCard>
