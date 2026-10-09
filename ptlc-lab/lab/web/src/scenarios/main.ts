import { createApp } from 'vue'

// The same two faces as the console, shipped with the page for the same
// reason: the lab often has no route to a font service.
import '@fontsource-variable/space-grotesk'
import '@fontsource-variable/jetbrains-mono'
import '../style.css'
import './scenarios.css'

import App from './App.vue'
import { start } from './store.ts'

void start()
createApp(App).mount('#app')
