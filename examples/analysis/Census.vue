<template>
    <v-container>
        <v-overlay class="align-center justify-center" :model-value="loading" persistent>
            <div class="d-flex justify-center align-center">
                <v-progress-circular indeterminate model-value="20"></v-progress-circular>
            </div>
            <div class="mt-3">Loading</div>
        </v-overlay>
        <v-app-bar color="grey-lighten-2" flat>
            <div class="ml-6 pl-6 font-weight-bold">
                <v-breadcrumbs>
                    <v-breadcrumbs-item>選擇資料庫</v-breadcrumbs-item>
                </v-breadcrumbs>
            </div>
        </v-app-bar>
        <v-row>
            <v-col cols="4">
                <v-card color="grey-lighten-2" flat variant="outlined">
                    <v-list @update:selected="selectDoc($event)" density="compact" mandatory return-object>
                        <template v-for="(type, idx) in types">
                            <v-list-subheader class="font-weight-bold" color="black" style="font-size: 15px">
                                {{ type.title }}
                            </v-list-subheader>
                            <v-list-item v-for="(item, idx) in showDocs[type.key]" :active="item.selected" :key="idx" :value="item" color="primary" variant="plain">
                                <v-list-item-title v-text="item.for.title" style="font-size: 14px"></v-list-item-title>
                            </v-list-item>
                            <v-divider class="my-2" v-if="idx != types.length - 1"></v-divider>
                        </template>
                    </v-list>
                </v-card>
            </v-col>
            <v-col cols="8">
                <census-info v-if="selectedDocIdx >= 0" :doc="docs[selectedDocIdx]"></census-info>
            </v-col>
        </v-row>
    </v-container>
</template>

<script>
import axios from 'axios'
import CensusInfo from './CensusInfo.vue'

export default {
    components: { CensusInfo },
    data() {
        return {
            loading: false,
            projectTypes: {
                1: [
                    { key: 'C10', title: '高一專一學生' },
                    { key: 'C11', title: '高二專二學生' },
                    { key: 'C12', title: '高三學生調查' },
                    { key: 'CT', title: '學校人員調查' },
                ],
                2: [
                    { key: 'GT0', title: '新進師資生調查' },
                    { key: 'FT', title: '實習師資生調查' },
                    { key: 'FF', title: '修畢師資生調查' },
                ],
            },
            selectedDocIdx: -1,
            docs: [],
            types: [],
        }
    },
    created() {
        this.getAllCensus()
    },
    methods: {
        getAllCensus() {
            this.loading = true
            axios
                .get('allCensus')
                .then((response) => {
                    this.docs = response.data.docs.sort((a, b) => a.analysis.code_year - b.analysis.code_year)
                    this.selectedDocIdx = this.docs.findIndex((doc) => doc.selected)
                    this.types = this.projectTypes[response.data.project]
                })
                .finally(() => (this.loading = false))
        },
        selectDoc($event) {
            this.docs[this.selectedDocIdx].selected = false
            this.selectedDocIdx = this.docs.findIndex((doc) => doc.id === $event[0].id)
            this.docs[this.selectedDocIdx].selected = true
        },
    },
    computed: {
        showDocs() {
            return this.docs.reduce((groups, doc) => {
                const target = doc.analysis.target_people
                if (!groups.hasOwnProperty(target)) {
                    groups[target] = []
                }
                groups[target].push(doc)
                return groups
            }, {})
        },
    },
}
</script>
