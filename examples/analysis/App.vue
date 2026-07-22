<template>
    <v-app>
        <v-main>
            <v-container fluid>
                <router-view />
            </v-container>
        </v-main>
        <v-navigation-drawer location="right" permanent width="360" class="chat-drawer">
            <div class="chat-panel">
                <div class="chat-panel-messages">
                    <div v-if="messages.length === 0" class="text-grey text-caption">尚無訊息</div>
                    <div v-else class="chat-messages">
                        <div v-for="(m,i) in messages" :key="i" :class="['bubble-group', m.role === 'user' ? 'mine' : '']">
                            <div v-if="m.role === 'assistant'" class="bubble bubble-markdown" v-html="renderMarkdown(m.text)"></div>
                            <div v-else class="bubble mine">{{ m.text }}</div>
                        </div>
                    </div>
                </div>
                <div class="composer-row">
                    <v-text-field v-model="input" placeholder="輸入訊息..." density="compact" hide-details variant="plain"
                        class="composer-input" @keyup.enter="send" />
                    <button class="composer-send-btn" :disabled="!input.trim()" aria-label="送出" @click="send">
                        <v-icon icon="mdi-send" size="18" />
                    </button>
                </div>
            </div>
        </v-navigation-drawer>
    </v-app>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, provide } from 'vue'
import { AgentBridge, defineTool } from '@onagent/bridge'
import type { ErrorPayload } from '@onagent/bridge'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import './chat.css'

marked.setOptions({ breaks: true, gfm: true })

// Assistant messages are LLM output rendered as Markdown via v-html;
// marked doesn't sanitize its own output, so DOMPurify is mandatory
// here even though the content isn't directly user-controlled.
function renderMarkdown(text: string): string {
    return DOMPurify.sanitize(marked.parse(text, { async: false }))
}

interface ChatMessage {
    role: 'user' | 'assistant'
    text: string
}

interface Question {
    name: string
    title: string
}

type SelectQuestionHandler = (names: string[], selected?: boolean, clear?: boolean) => void

interface SelectQuestionArgs {
    names?: string[]
    selected?: boolean
    clear?: boolean
}

// Genuine validation, not a bare `as Args` assertion — a hallucinated tool
// call with e.g. names as a single string instead of an array is caught
// here (result: names is dropped, clear/selected still apply) rather than
// reaching Menu.vue's onSelectQuestion handler in a shape it doesn't expect.
function parseSelectQuestionArgs(raw: unknown): SelectQuestionArgs {
    const r = (raw ?? {}) as Record<string, unknown>
    return {
        names: Array.isArray(r.names) ? r.names.filter((n): n is string => typeof n === 'string') : undefined,
        selected: typeof r.selected === 'boolean' ? r.selected : undefined,
        clear: typeof r.clear === 'boolean' ? r.clear : undefined,
    }
}

interface ListQuestionsArgs {
    limit?: number
    offset?: number
}

function parseListQuestionsArgs(raw: unknown): ListQuestionsArgs {
    const r = (raw ?? {}) as Record<string, unknown>
    return {
        limit: typeof r.limit === 'number' ? r.limit : undefined,
        offset: typeof r.offset === 'number' ? r.offset : undefined,
    }
}

const AGENT_WS_URL = import.meta.env.VITE_AGENT_WS_URL ?? 'ws://localhost:18080/ws'
const AGENT_API_KEY = import.meta.env.VITE_AGENT_API_KEY

const input = ref('')
const messages = ref<ChatMessage[]>([])
const connecting = ref(true)
const questions = ref<Question[]>([])
const selectQuestionHandler = ref<SelectQuestionHandler | null>(null)
let bridge: AgentBridge | null = null

function connect() {
    bridge = new AgentBridge({
        url: AGENT_WS_URL,
        appId: 'analysis',
        apiKey: AGENT_API_KEY,
        onAssistantMessage: (text) => {
            messages.value.push({ role: 'assistant', text })
        },
        onError: (err: ErrorPayload) => {
            messages.value.push({ role: 'assistant', text: `[錯誤] ${err.message}` })
        },
        tools: [
            defineTool('select_question', parseSelectQuestionArgs, ({ names, selected, clear }) => {
                // clear and names/selected aren't mutually exclusive: the
                // model sometimes sends both in one call (e.g. "just pick
                // p3q1c2" comes back as clear=true + names=['p3q1c2']
                // selected=true, apparently meaning "replace the whole
                // selection with just this"). Applying clear first, then
                // names/selected as a second call on the now-empty
                // selection, makes both orders of intent work: "clear"
                // alone still clears (no names means the second call is a
                // no-op), and "clear + select" replaces the selection
                // instead of the names/selected half silently getting
                // dropped.
                if (clear) {
                    selectQuestionHandler.value?.([], false, true)
                }
                if (names && names.length > 0) {
                    selectQuestionHandler.value?.(names, selected, false)
                }
            }),
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
            defineTool('list_questions', parseListQuestionsArgs, ({ limit, offset }) => {
                const all = questions.value.map((q) => ({ name: q.name, title: q.title }))
                const start = offset ?? 0
                return all.slice(start, start + (limit ?? all.length))
            }),
        ],
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

provide('setQuestions', (qs: Question[]) => {
    questions.value = qs
    // No push-to-backend call here on purpose: list_questions (a
    // query tool, see tools.yaml) lets the LLM pull this same data
    // on demand instead, so it never gets spliced into the raw
    // user prompt text — see the thought field's instructions to
    // call list_questions before select_question.
})
provide('onSelectQuestion', (handler: SelectQuestionHandler) => { selectQuestionHandler.value = handler })
</script>
