import { createApp } from 'vue'

// The design system's two faces, Space Grotesk for words and JetBrains Mono
// for everything the chain said. Both ship with the page: the lab runs on a
// devnet in a container, often with no route to a font service.
import '@fontsource-variable/space-grotesk'
import '@fontsource-variable/jetbrains-mono'
import './style.css'

import App from './App.vue'
import { start } from './store.ts'

start()
createApp(App).mount('#app')
