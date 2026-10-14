import assert from 'node:assert/strict';
import {buildObjectHierarchy} from '../web/object-menu.js';

const meshes = name => [{id:'0',name,visible:true,polygons:10}];
const models = [
  {id:'arms',name:'Arms',parentId:null,bones:2,meshes:meshes('Arms mesh'),boneHierarchy:[
    {id:'root',name:'root',parentIndex:-1},{id:'hand',name:'hand',parentIndex:0}]},
  {id:'gun',name:'Gun',parentId:'arms',bones:2,meshes:meshes('Gun mesh'),boneHierarchy:[
    {id:'gun-root',name:'gun-root',parentIndex:-1},{id:'bolt',name:'bolt',parentIndex:0}]},
  {id:'scope',name:'Scope',parentId:'gun',bones:1,meshes:meshes('Scope mesh')},
  {id:'pivot',name:'Pivot',parentId:'arms',bones:1,meshes:[]},
  {id:'marker',name:'Marker',parentId:'pivot',bones:0,meshes:meshes('Marker mesh')},
];
const original = JSON.stringify(models);
const tree = buildObjectHierarchy(models);
const children = node => node.children.filter(child => child.kind === 'model');
assert.equal(tree.length,1);
assert.equal(tree[0].modelId,'arms');
assert.deepEqual(children(tree[0]).map(node=>node.modelId),['gun','pivot']);
assert.equal(children(children(tree[0])[0])[0].modelId,'scope');
assert.equal(children(children(tree[0])[1])[0].modelId,'marker');
assert.equal(children(tree[0])[1].object.meshes.length,0,'Mesh-free attachment parents stay in the hierarchy.');
const skeleton = tree[0].children.find(node=>node.kind==='skeleton');
assert.equal(skeleton.children.length,1);
assert.equal(skeleton.children[0].boneId,'root');
assert.equal(skeleton.children[0].children[0].boneId,'hand');
assert.equal(skeleton.children[0].children[0].boneIndex,1);
assert.equal(tree[0].children.find(node=>node.kind==='mesh').meshId,'0');
assert.equal(JSON.stringify(models),original,'Building the tree never changes source objects.');
assert.deepEqual(buildObjectHierarchy([]),[]);
assert.equal(buildObjectHierarchy([{id:'same',name:'A',meshes:[]},{id:'same',name:'B',meshes:[]}]).length,1);
assert.equal(buildObjectHierarchy([{name:'No id',meshes:[]}]).length,0);

// Explicit invalid links are displayed as roots. No role or relationship is
// guessed from matching names, skeleton sizes, order or repeated joint IDs.
const invalid = buildObjectHierarchy([
  {id:'a',name:'Shared',parentId:'b',bones:19,meshes:[]},
  {id:'b',name:'Shared',parentId:'a',bones:19,meshes:[]},
  {id:'c',name:'Shared',parentId:'missing',bones:19,meshes:[]},
  {id:'d',name:'Shared',parentId:'d',bones:19,meshes:[]},
]);
assert.deepEqual(invalid.map(node=>node.modelId),['a','b','c','d']);
assert.ok(invalid.every(node=>children(node).length===0));
const badBones = buildObjectHierarchy([{id:'bad-bones',meshes:[],bones:5,boneHierarchy:[
  {id:0,name:'Self',parentIndex:0}, {id:1,name:'Cycle one',parentIndex:2},
  {id:2,name:'Cycle two',parentIndex:1}, {id:3,name:'Missing',parentIndex:99},
  {id:4,name:'Unknown',parentIndex:'0'},
]}])[0].children[0];
assert.equal(badBones.children.length,5);
assert.ok(badBones.children.every(bone=>bone.children.length===0));
console.log('Object hierarchy: 19 checks passed.');
