import { createApp, ref, onMounted, onUnmounted, provide } from 'vue'
import { createVuetify } from 'vuetify'
import { createRouter, createWebHistory } from 'vue-router'
import * as components from 'vuetify/components'
import * as directives from 'vuetify/directives'
import 'vuetify/styles'

import moment from 'moment'
import Menu from './components/Menu.vue'
import { AgentBridge } from '@agent-tool-platform/agent-bridge-sdk'

const AGENT_WS_URL = import.meta.env.VITE_AGENT_WS_URL ?? 'ws://localhost:18080/ws'
const AGENT_API_KEY = import.meta.env.VITE_AGENT_API_KEY

const router = createRouter({
    history: createWebHistory(),
    routes: [
        { path: '/', redirect: '/analysis/1/menu' },
        { path: '/analysis/:doc/menu', component: Menu },
    ],
})

const vuetify = createVuetify({ components, directives })

const App = {
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
                appId: 'analysis-app',
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
            // Keep the backend's grounding context in sync with the real
            // question checklist as soon as it loads, so any prompt (not
            // just the first one) can be answered using real data.
            bridge?.sendContext({ availableQuestions: qs.map(q => ({ name: q.name, title: q.title })) })
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

const app = createApp(App)

app.config.globalProperties.$moment = {
    dateFormat(date) {
        return moment(date).format('yyyy-MM-DD')
    },
}

app.use(router)
app.use(vuetify)
app.mount('#app')
