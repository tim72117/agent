<template>
    <v-container>
        <v-overlay class="align-center justify-center" :model-value="loading" persistent>
            <div class="d-flex justify-center align-center">
                <v-progress-circular indeterminate model-value="20"></v-progress-circular>
            </div>
            <div class="mt-3">Loading</div>
        </v-overlay>
        <v-app-bar color="grey-lighten-2" flat>
            <div class="ml-6 pl-6">
                <v-breadcrumbs :items="breadcrumbs">
                    <template v-slot:title="{ item }">
                        <v-breadcrumbs-item :active="item.disabled" active-color="black" color="#4183C4">
                            {{ item.title }}
                        </v-breadcrumbs-item>
                    </template>
                    <template v-slot:divider>
                        <v-icon icon="mdi-chevron-right"></v-icon>
                    </template>
                </v-breadcrumbs>
            </div>
        </v-app-bar>
        <v-card color="grey-lighten-2" flat height="650" variant="outlined">
            <v-card-title class="text-black d-flex align-center">
                <div style="width: 300px">
                    <v-text-field
                        v-model="search"
                        append-inner-icon="mdi-magnify"
                        color="#82B1FF"
                        density="comfortable"
                        hide-details
                        placeholder="搜尋關鍵字..."
                        variant="outlined"
                    ></v-text-field>
                </div>
                <v-btn class="mx-2" color="#E0E1E2" to="open" variant="flat">
                    <span class="mx-1" style="font-size: 15px">上一步</span>
                </v-btn>
                <v-btn class="text-white mx-2" @click="nextStep" color="#B5CC18" variant="flat">
                    <span class="mx-1" style="font-size: 15px">下一步</span>
                </v-btn>
            </v-card-title>
            <v-divider color="grey-darken-4"></v-divider>
            <v-card-text class="text-black">
                <v-data-table-virtual
                    class="custom-table"
                    v-model="selected"
                    :headers="headers"
                    :items="columns"
                    :search="search"
                    height="500"
                    hide-no-data
                    item-value="name"
                    return-object
                    show-select
                >
                    <template v-slot:top="{ selectAll, allSelected, someSelected }">
                        <v-toolbar class="pl-2">
                            <v-checkbox-btn :indeterminate="!allSelected && someSelected" :model-value="allSelected" @click="selectAll(!allSelected)">
                                <template v-slot:label>
                                    全選
                                    <span style="font-size: 14px">(勾選題目時，建議您參考問卷，以完整瞭解題目原意！)</span>
                                </template>
                            </v-checkbox-btn>
                        </v-toolbar>
                    </template>
                    <template v-slot:headers></template>
                    <template v-slot:item.actions="{ item }">
                        <option-definition-dialog :question="item"></option-definition-dialog>
                    </template>
                </v-data-table-virtual>
            </v-card-text>
        </v-card>
        <form id="form-columns" action="analysis" method="post" style="display: none">
            <input :value="csrf" name="_token" type="hidden" />
            <input v-for="column in selected" v-model="column.name" name="columns_choosed[]" type="text" />
        </form>
    </v-container>
</template>

<script>
import { inject } from 'vue'
import OptionDefinitionDialog from './OptionDefinitionDialog.vue'
import CensusFooter from './CensusFooter.vue'
import { questions } from '../data/questions.js'

export default {
    components: { CensusFooter, OptionDefinitionDialog },
    setup() {
        const onSelectQuestion = inject('onSelectQuestion', null)
        return { setQuestions: inject('setQuestions', null), onSelectQuestion }
    },
    data() {
        return {
            loading: false,
            breadcrumbs: [
                {
                    title: '選擇資料庫',
                    disabled: false,
                    href: 'open',
                },
                {
                    title: '選擇題目',
                    disabled: true,
                    href: 'menu',
                },
            ],
            headers: [
                { title: '', sortable: false, key: 'title', align: 'start' },
                { title: '', sortable: false, key: 'actions' },
            ],
            selected: [],
            columns: [],
            search: '',
            csrf: document.querySelector('meta[name="csrf-token"]').getAttribute('content'),
        }
    },
    created() {
        this.getColumns()
    },
    mounted() {
        this.onSelectQuestion?.((names, selected, clear) => {
            if (clear) {
                this.selected = []
                return
            }
            const nameSet = new Set(names)
            if (selected) {
                const toAdd = this.columns.filter(q => nameSet.has(q.name) && !this.selected.find(s => s.name === q.name))
                this.selected = [...this.selected, ...toAdd]
            } else {
                this.selected = this.selected.filter(q => !nameSet.has(q.name))
            }
        })
    },
    methods: {
        getColumns() {
            this.columns = questions
            this.selected = questions.filter((question) => question.choosed)
            this.setQuestions?.(questions)
        },
        nextStep() {
            if (this.selected.length > 0) {
                document.getElementById('form-columns').submit()
            }
        },
    },
}
</script>

<style scoped>
:deep(.v-breadcrumbs-item--disabled) {
    opacity: 1;
    font-weight: bold;
}
</style>
