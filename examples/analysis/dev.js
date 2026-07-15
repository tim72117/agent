import { createApp, ref, onMounted, onUnmounted, provide } from 'vue'
import { createVuetify } from 'vuetify'
import { createRouter, createWebHistory } from 'vue-router'
import * as components from 'vuetify/components'
import * as directives from 'vuetify/directives'
import 'vuetify/styles'

import moment from 'moment'
import Menu from './components/Menu.vue'
import { AgentBridge } from '@onagent/bridge'

const AGENT_WS_URL = import.meta.env.VITE_AGENT_WS_URL ?? 'ws://localhost:18080/ws'
const AGENT_API_KEY = import.meta.env.VITE_AGENT_API_KEY

export const router = createRouter({
    history: createWebHistory(),
    routes: [
        { path: '/', redirect: '/analysis/1/menu' },
        { path: '/analysis/:doc/menu', component: Menu },
    ],
})

export const vuetify = createVuetify({ components, directives })

export const App = {
    setup() {
        const input = ref('')
        const messages = ref([])
        const connecting = ref(true)
        const questions = ref([])
        const selectQuestionHandler = ref(null)
        let bridge = null

        function connect() {
            bridge = new AgentBridge({
                url: AGENT_WS_URL,
                appId: 'analysis',
                apiKey: AGENT_API_KEY,
                onAssistantMessage: (text) => {
                    messages.value.push({ role: 'assistant', text })
                },
                onError: (err) => {
                    messages.value.push({ role: 'assistant', text: `[錯誤] ${err.message}` })
                },
                tools: {
                    select_question: ({ names, selected, clear }) => {
                        if (clear) {
                            selectQuestionHandler.value?.([], false, true)
                            return
                        }
                        selectQuestionHandler.value?.(names ?? [], selected, false)
                    },
                    // kind: query on the backend (see tools.yaml) — this
                    // return value is awaited and fed back into the LLM's
                    // reasoning, not fire-and-forget like select_question
                    // above. Reads the same questions.value the page
                    // set via setQuestions, so the LLM can map
                    // a user's natural-language request to a question's name.
                    // limit is required (not just declared optional) because
                    // of a vLLM streaming quirk: a tool call whose arguments
                    // end up empty ("{}") loses its name/id in the first
                    // streamed chunk, making it unparseable — see
                    // docs/TODO-want-registry-append-only.md's "附帶發現".
                    list_questions: ({ limit }) => {
                        const all = questions.value.map(q => ({ name: q.name, title: q.title }))
                        return all.slice(0, limit ?? all.length)
                    },
                },
            })
            connecting.value = false
        }

        function send() {
            const text = input.value.trim()
            if (!text || !bridge) return
            messages.value.push({ role: 'user', text })
            bridge.prompt(text)
            input.value = ''
        }

        onMounted(connect)
        onUnmounted(() => bridge?.close())

        provide('setQuestions', (qs) => {
            questions.value = qs
            // No push-to-backend call here on purpose: list_questions (a
            // query tool, see tools.yaml) lets the LLM pull this same data
            // on demand instead, so it never gets spliced into the raw
            // user prompt text — see the thought field's instructions to
            // call list_questions before select_question.
        })
        provide('onSelectQuestion', (handler) => { selectQuestionHandler.value = handler })

        return { input, messages, send, connecting }
    },
    template: `
        <v-app>
            <v-main style="padding-bottom:140px">
                <v-container fluid>
                    <router-view />
                </v-container>
            </v-main>
            <v-footer app height="auto" style="flex-direction:column;padding:0">
                <v-card flat tile width="100%" border="t">
                    <v-card-text style="max-height:160px;overflow-y:auto;padding:8px 16px">
                        <div v-if="messages.length === 0" class="text-grey text-caption">尚無訊息</div>
                        <div v-for="(m,i) in messages" :key="i" :class="m.role === 'user' ? 'text-right' : 'text-left'" class="mb-1">
                            <v-chip size="small" :color="m.role === 'user' ? 'blue' : 'grey-darken-1'" label>{{ m.text }}</v-chip>
                        </div>
                    </v-card-text>
                    <v-divider />
                    <v-card-actions style="padding:8px 16px">
                        <v-text-field v-model="input" placeholder="輸入訊息..." density="compact" hide-details variant="outlined"
                            @keyup.enter="send" class="mr-2" />
                        <v-btn color="primary" variant="flat" @click="send">送出</v-btn>
                    </v-card-actions>
                </v-card>
            </v-footer>
        </v-app>
    `,
}

// Real bootstrap only — skipped under Vitest (import.meta.env.MODE ===
// "test" there by default), so tests can import App/router above and mount
// them however the test needs without this module also mounting a second,
// untestable instance into a real "#app" element as a side effect of the
// import itself.
if (import.meta.env.MODE !== 'test') {
    const app = createApp(App)

    app.config.globalProperties.$moment = {
        dateFormat(date) {
            return moment(date).format('yyyy-MM-DD')
        },
    }

    app.use(router)
    app.use(vuetify)
    app.mount('#app')
}
