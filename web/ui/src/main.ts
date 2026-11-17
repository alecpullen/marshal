import '@fontsource-variable/geist'
import '@fontsource-variable/geist-mono'
import './app.css'
import { mount } from 'svelte'
import App from './App.svelte'
import { adoptTokenFromURL } from './lib/api'

/*
  Before mounting, so the very first request carries the token: `marshal studio`
  hands the daemon's credential over as `?token=`, and this moves it into
  sessionStorage and out of the address bar. See adoptTokenFromURL.
*/
adoptTokenFromURL()

mount(App, { target: document.getElementById('app')! })
