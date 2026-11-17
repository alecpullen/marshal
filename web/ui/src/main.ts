import '@fontsource-variable/geist'
import '@fontsource-variable/geist-mono'
import './app.css'
import { mount } from 'svelte'
import App from './App.svelte'
import { adoptCodeFromURL, adoptTokenFromURL } from './lib/api'

/*
  Before mounting, so the very first request carries the token. `marshal studio`
  hands over a single-use code in the fragment (`#code=<code>`), which this
  exchanges for the daemon's token; a `?token=` a person pasted themselves is
  still adopted. Both leave nothing usable in the address bar. See
  adoptCodeFromURL and adoptTokenFromURL.
*/
void (async () => {
  await adoptCodeFromURL()
  adoptTokenFromURL()
  mount(App, { target: document.getElementById('app')! })
})()
